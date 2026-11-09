#!/usr/bin/env python3
"""Update the existing single-owner GKE deployment without publishing runtime data."""
import argparse
import json
import re
import subprocess
import sys
import time
from pathlib import Path

STATUS = r'''
import json, pathlib, sqlite3, urllib.request
try:
 version=json.load(urllib.request.urlopen('http://127.0.0.1:7341/healthz',timeout=10))['version']
except Exception:
 version=None
path=pathlib.Path('/var/lib/machinist/tickets.db')
tickets,runs={},0
if path.is_file():
 with sqlite3.connect('file:'+str(path)+'?mode=ro',uri=True) as db:
  tickets=dict(db.execute('select id,state from tickets'))
  runs=db.execute("select count(*) from runs where status in ('queued','running')").fetchone()[0]
print(json.dumps({'version':version,'tickets':tickets,'active_runs':runs}))
'''
BACKUP = r'''
import pathlib, sqlite3, sys, os
root=pathlib.Path('/var/lib/machinist')
source_path=next((root/name for name in ['tickets.db','jobs.db','factory.db'] if (root/name).is_file()),None)
if source_path is None: raise RuntimeError('No authoritative SQLite database exists')
target=root/'deployments'/sys.argv[1]
target.mkdir(parents=True,exist_ok=False,mode=0o700)
copy=target/source_path.name
with sqlite3.connect('file:'+str(source_path)+'?mode=ro',uri=True) as source, sqlite3.connect(copy) as backup:
 source.backup(backup)
 if backup.execute('pragma integrity_check').fetchone()[0]!='ok': raise RuntimeError('SQLite backup integrity check failed')
os.chmod(copy,0o600)
(target/'release.json').write_text(sys.argv[2])
print('Private SQLite backup completed; runtime will be captured by the disk snapshot.')
'''


def pinned(image):
    if not re.fullmatch(r'[a-zA-Z0-9./:_-]+@sha256:[a-f0-9]{64}', image):
        raise ValueError('Images must be pinned by sha256 digest')
    return image


def require_idle(status):
    if status['active_runs']:
        raise RuntimeError('Deployment postponed: finish running work first')


def preserved(before, after):
    if not set(before['tickets']) <= set(after['tickets']):
        raise RuntimeError('Saved tickets changed during deployment')


def with_runner_image(text, image):
    updated,count=re.subn(r'(?m)^image[ \t]*=.*$','image = '+json.dumps(image),text)
    if count!=1:
        raise ValueError('Expected one image setting in runner.toml')
    return updated


class Deployment:
    def __init__(self, context, snapshot_backend="kubernetes"):
        self.context = context
        self.snapshot_backend = snapshot_backend

    def k(self, *args, namespace='machinist-system', data=None, timeout=360):
        result=subprocess.run(['kubectl','--context',self.context,'-n',namespace,*args],input=json.dumps(data) if data is not None else None,text=True,capture_output=True,timeout=timeout)
        if result.returncode:
            raise RuntimeError('kubectl '+args[0]+' failed: '+result.stderr.strip())
        return result.stdout

    def get(self, kind, name=None, **kwargs):
        return json.loads(self.k('get',kind,*([name] if name else []),'-o','json',**kwargs))

    def status(self):
        return json.loads(self.k('exec','deployment/machinist-control-plane','--','python3','-c',STATUS))

    def no_runners(self):
        for kind,selector in [('jobs','app.kubernetes.io/managed-by=machinist'),('pods','app=machinist-agent')]:
            objects=json.loads(self.k('get',kind,'-l',selector,'-o','json',namespace='machinist-runners'))
            if objects['items']:
                raise RuntimeError('Runner '+kind+' remain; do not bypass uncertain process ownership')

    def snapshot(self, name):
        if self.snapshot_backend == 'gcloud':
            claim=self.get('pvc','machinist-state')
            volume=self.get('pv',claim['spec']['volumeName'])
            csi=volume['spec']['csi']
            match=re.fullmatch(r'projects/([^/]+)/zones/([^/]+)/disks/([^/]+)',csi['volumeHandle'])
            if csi['driver']!='pd.csi.storage.gke.io' or not match:
                raise RuntimeError('Operator snapshots require a zonal GKE Persistent Disk')
            project,zone,disk=match.groups()
            subprocess.run(['gcloud','compute','disks','snapshot',disk,'--project',project,'--zone',zone,'--snapshot-names',name,'--storage-location',zone.rsplit('-',1)[0],'--quiet'],check=True,capture_output=True,text=True,timeout=360)
            status=subprocess.check_output(['gcloud','compute','snapshots','describe',name,'--project',project,'--format=value(status)'],text=True,timeout=30).strip()
            if status!='READY':raise RuntimeError('Operator disk snapshot is not READY')
            return
        snapshot={'apiVersion':'snapshot.storage.k8s.io/v1','kind':'VolumeSnapshot','metadata':{'name':name},'spec':{'volumeSnapshotClassName':'machinist-state-retain','source':{'persistentVolumeClaimName':'machinist-state'}}}
        self.k('create','-f','-',data=snapshot)
        self.k('wait','--for=jsonpath={.status.readyToUse}=true','volumesnapshot/'+name,'--timeout=300s')

    def run(self, control_plane, runner, revision, output):
        pinned(control_plane); pinned(runner)
        if not re.fullmatch('[a-f0-9]{40}',revision):
            raise ValueError('Revision must be a full commit SHA')
        before=self.status(); require_idle(before); self.no_runners()
        old=self.get('deployment','machinist-control-plane')
        if old['spec']['replicas']!=1 or old['spec']['strategy']['type']!='Recreate':
            raise RuntimeError('Expected one Recreate control-plane owner')
        old_image=old['spec']['template']['spec']['containers'][0]['image']
        proxy=self.get('deployment','machinist-egress',namespace='machinist-runners')
        name='machinist-deploy-'+revision[:8]+'-'+str(int(time.time()))
        release={'revision':revision,'control_plane':control_plane,'runner':runner,'previous_control_plane':old_image,'previous_proxy':proxy['spec']['template']['spec']['containers'][0]['image'],'snapshot':name,'snapshot_backend':self.snapshot_backend,'tickets_preserved':len(before['tickets'])}
        self.k('scale','deployment/machinist-runner','--replicas=0')
        self.k('wait','--for=delete','pod','-l','app=machinist-runner','--timeout=180s')
        self.k('scale','deployment/machinist-control-plane','--replicas=0')
        self.k('wait','--for=delete','pod','-l','app=machinist-control-plane','--timeout=180s')
        self.no_runners()
        security={'runAsNonRoot':True,'runAsUser':1000,'runAsGroup':1000,'fsGroup':1000,'seccompProfile':{'type':'RuntimeDefault'}}
        pod={'apiVersion':'v1','kind':'Pod','metadata':{'name':name},'spec':{'restartPolicy':'Never','activeDeadlineSeconds':900,'automountServiceAccountToken':False,'securityContext':security,'containers':[{'name':'utility','image':old_image,'command':['python3','-c','import time; time.sleep(900)'],'securityContext':{'allowPrivilegeEscalation':False,'readOnlyRootFilesystem':True,'capabilities':{'drop':['ALL']}},'resources':{'requests':{'cpu':'250m','memory':'512Mi','ephemeral-storage':'1Gi'},'limits':{'cpu':'250m','memory':'512Mi','ephemeral-storage':'1Gi'}},'volumeMounts':[{'name':'state','mountPath':'/var/lib/machinist'}]}],'volumes':[{'name':'state','persistentVolumeClaim':{'claimName':'machinist-state'}}]}}
        # Once stopped, failures deliberately leave the owner stopped. Never
        # restart an old binary against potentially migrated or changed state.
        try:
            self.k('create','-f','-',data=pod)
            self.k('wait','--for=condition=Ready','pod/'+name,'--timeout=300s')
            self.k('exec',name,'--','python3','-c',BACKUP,name,json.dumps(release))
            self.snapshot(name)
        finally:
            self.k('delete','pod',name,'--ignore-not-found=true','--wait=true','--timeout=180s')
        config=self.get('configmap','machinist-runner-config')
        self.k('patch','configmap/machinist-runner-config','--type=merge','-p',json.dumps({'data':{'runner.toml':with_runner_image(config['data']['runner.toml'],runner)}}))
        self.k('set','image','deployment/machinist-egress','proxy='+runner,namespace='machinist-runners')
        self.k('rollout','status','deployment/machinist-egress','--timeout=300s',namespace='machinist-runners')
        self.k('set','image','deployment/machinist-control-plane','machinist='+control_plane)
        self.k('set','image','deployment/machinist-runner','runner='+control_plane)
        self.k('annotate','deployment/machinist-control-plane','machinist.dev/revision='+revision,'machinist.dev/state-snapshot='+name,'--overwrite')
        try:
            self.k('scale','deployment/machinist-control-plane','--replicas=1')
            self.k('rollout','status','deployment/machinist-control-plane','--timeout=300s')
            expected=time.monotonic()+90
            while True:
                after=self.status()
                if after['version']==revision:break
                if time.monotonic()>expected:raise RuntimeError('The control plane did not report this release')
                time.sleep(3)
            preserved(before,after)
            self.k('scale','deployment/machinist-runner','--replicas=1')
            self.k('rollout','status','deployment/machinist-runner','--timeout=300s')
        except (RuntimeError, ValueError, subprocess.SubprocessError):
            # Quarantine an unverified release without restoring an old binary
            # over state that may already have migrated. Preserve runner evidence.
            self.k('scale','deployment/machinist-runner','--replicas=0')
            self.k('scale','deployment/machinist-control-plane','--replicas=0')
            raise
        Path(output).write_text(json.dumps(release,indent=2)+'\n')
        print(json.dumps(release,indent=2))


if __name__=='__main__':
    p=argparse.ArgumentParser(description=__doc__)
    p.add_argument('--context',required=True)
    p.add_argument('--control-plane',required=True,type=pinned)
    p.add_argument('--runner',required=True,type=pinned)
    p.add_argument('--revision',required=True)
    p.add_argument('--output',default='release.json')
    p.add_argument('--snapshot-backend',choices=['kubernetes','gcloud'],default='kubernetes',help='gcloud is for existing operator access; CI uses Kubernetes snapshots')
    a=p.parse_args()
    try:Deployment(a.context,a.snapshot_backend).run(a.control_plane,a.runner,a.revision,a.output)
    except (RuntimeError,ValueError,subprocess.SubprocessError) as error:
        print(str(error),file=sys.stderr)
        print('Deployment stopped. Inspect the current owner and snapshot before recovery; no state was discarded.',file=sys.stderr)
        sys.exit(1)
