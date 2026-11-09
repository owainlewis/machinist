import copy
import importlib.util
import json
import subprocess
import sqlite3
import contextlib
import io
import tempfile
import textwrap
import os
import unittest
from unittest.mock import patch
from pathlib import Path

spec=importlib.util.spec_from_file_location('gke_deploy',Path(__file__).with_name('deploy.py'))
deploy=importlib.util.module_from_spec(spec)
spec.loader.exec_module(deploy)
IMAGE='registry/image@sha256:'+'a'*64
REVISION='a'*40
STATE={'version':REVISION,'tickets':{'t_1':'pr_ready'},'active_runs':0}
RUNNER_CONFIG='control_plane = "http://x"\n\n[kubernetes]\nimage = "old@sha256:'+'b'*64+'"\nsecret = "keep-me"\n'


class FakeDeployment(deploy.Deployment):
    def __init__(self, fail=None):
        super().__init__('test'); self.calls=[]; self.fail=fail
    def status(self):return copy.deepcopy(STATE)
    def get(self,*args,**kwargs):return {'data':{'runner.toml':RUNNER_CONFIG},'spec':{'replicas':1,'strategy':{'type':'Recreate'},'template':{'spec':{'containers':[{'image':IMAGE}]}}}}
    def no_runners(self):self.calls.append(('no_runners',))
    def k(self,*args,**kwargs):
        self.calls.append(args)
        if self.fail and self.fail(args,kwargs):raise RuntimeError('injected failure')
        return ''


class DeploymentTests(unittest.TestCase):
    def test_active_work_prevents_any_mutation(self):
        fake=FakeDeployment(); active=copy.deepcopy(STATE); active['active_runs']=1
        fake.status=lambda:active
        with self.assertRaises(RuntimeError):fake.run(IMAGE,IMAGE,REVISION,'unused')
        self.assertEqual(fake.calls,[])

    def test_saved_tickets_must_survive_and_new_ones_are_allowed(self):
        after=copy.deepcopy(STATE);after['tickets']={}
        with self.assertRaises(RuntimeError):deploy.preserved(STATE,after)
        after=copy.deepcopy(STATE);after['tickets']['t_2']='working'
        deploy.preserved(STATE,after)

    def test_snapshot_failure_never_changes_images_or_restarts_owner(self):
        fake=FakeDeployment(lambda args,kw:args[0]=='wait' and any('volumesnapshot/' in a for a in args))
        with self.assertRaises(RuntimeError):fake.run(IMAGE,IMAGE,'a'*40,'unused')
        self.assertTrue(any(c[:2]==('delete','pod') for c in fake.calls))
        self.assertFalse(any(c[:2]==('set','image') for c in fake.calls))
        self.assertFalse(any('--replicas=1' in c for c in fake.calls))
        self.assertFalse(any(c[:1]==('patch',) for c in fake.calls))

    def test_backup_completes_before_configuration_and_rollout(self):
        fake=FakeDeployment()
        with tempfile.TemporaryDirectory() as temp:
            output=Path(temp)/'release.json'
            with contextlib.redirect_stdout(io.StringIO()):fake.run(IMAGE,'runner@sha256:'+'c'*64,REVISION,output)
            self.assertEqual(json.loads(output.read_text())['tickets_preserved'],1)
        calls=fake.calls
        backup=next(i for i,c in enumerate(calls) if deploy.BACKUP in c)
        ready=next(i for i,c in enumerate(calls) if any('volumesnapshot/' in a for a in c))
        delete=next(i for i,c in enumerate(calls) if c[:2]==('delete','pod'))
        config=next(i for i,c in enumerate(calls) if c[:1]==('patch',))
        restart=next(i for i,c in enumerate(calls) if '--replicas=1' in c)
        self.assertLess(backup,ready);self.assertLess(ready,delete);self.assertLess(delete,config);self.assertLess(config,restart)
        self.assertIn('runner@sha256:'+'c'*64,calls[config][4])

    def test_runner_stops_first_and_starts_last(self):
        fake=FakeDeployment()
        with tempfile.TemporaryDirectory() as temp, contextlib.redirect_stdout(io.StringIO()):
            fake.run(IMAGE,IMAGE,REVISION,Path(temp)/'release.json')
        scales=[c for c in fake.calls if c[0]=='scale']
        self.assertEqual([c[1] for c in scales],['deployment/machinist-runner','deployment/machinist-control-plane','deployment/machinist-control-plane','deployment/machinist-runner'])
        self.assertIn(('set','image','deployment/machinist-runner','runner='+IMAGE),fake.calls)

    def test_final_main_gate_skips_stale_revision_and_fails_closed(self):
        workflow=(Path(__file__).parents[2]/'.github/workflows/deploy-gke.yml').read_text()
        step=workflow.split('      - name: Back up, deploy and verify',1)[1]
        script=textwrap.dedent(step.split('        run: |\n',1)[1].split('          python3 deploy/kubernetes/deploy.py',1)[0])
        for current, failed, expected in [('a'*40,False,True),('b'*40,False,False),('',True,False)]:
            gh='gh() { return 1; }\n' if failed else 'gh() { printf %s '+current+'; }\n'
            result=subprocess.run(['bash','-c',gh+script+'printf MUTATED'],env={**os.environ,'REVISION':'a'*40,'GH_REPO':'owainlewis/machinist'},text=True,capture_output=True)
            self.assertEqual('MUTATED' in result.stdout,expected)
            self.assertEqual(result.returncode!=0,failed)

    def test_post_start_failures_stop_unverified_owner(self):
        for phase in ['rollout','history','runner']:
            fake=FakeDeployment()
            original_k=fake.k
            def k(*args, **kwargs):
                if phase=='rollout' and args[:3]==('rollout','status','deployment/machinist-control-plane'):
                    raise RuntimeError('rollout timed out')
                if phase=='runner' and args[:3]==('rollout','status','deployment/machinist-runner'):
                    raise RuntimeError('runner rollout timed out')
                return original_k(*args, **kwargs)
            fake.k=k
            if phase=='history':
                changed=copy.deepcopy(STATE); changed['tickets']={}
                states=iter([copy.deepcopy(STATE), changed]); fake.status=lambda: next(states)
            with tempfile.TemporaryDirectory() as temp:
                output=Path(temp)/'release.json'
                with self.assertRaises(RuntimeError):fake.run(IMAGE,IMAGE,REVISION,output)
                self.assertFalse(output.exists())
            self.assertEqual(fake.calls[-2:],[('scale','deployment/machinist-runner','--replicas=0'),('scale','deployment/machinist-control-plane','--replicas=0')])

    def test_wrong_version_never_counts_as_deployed(self):
        fake=FakeDeployment()
        old=copy.deepcopy(STATE);old['version']='old'
        fake.status=lambda:old
        with patch.object(deploy.time,'monotonic',side_effect=[0,1000]+[2000]*5), patch.object(deploy.time,'time',return_value=1):
            with tempfile.TemporaryDirectory() as temp:
                with self.assertRaises(RuntimeError):fake.run(IMAGE,IMAGE,REVISION,Path(temp)/'release.json')

    def test_operator_snapshot_requires_ready_without_granting_cluster_access(self):
        fake=FakeDeployment();fake.snapshot_backend='gcloud'
        def get(kind,name):
            if kind=='pvc':return {'spec':{'volumeName':'disk-pv'}}
            return {'spec':{'csi':{'driver':'pd.csi.storage.gke.io','volumeHandle':'projects/test/zones/europe-west2-b/disks/state'}}}
        fake.get=get
        with patch.object(deploy.subprocess,'run') as run, patch.object(deploy.subprocess,'check_output',return_value='CREATING'):
            with self.assertRaises(RuntimeError):fake.snapshot('backup')
            self.assertEqual(run.call_args.args[0][:4],['gcloud','compute','disks','snapshot'])
        self.assertEqual(fake.calls,[])

    def test_backup_supports_old_and_current_database_without_mutating_source(self):
        for name in ['tickets.db','jobs.db','factory.db']:
            with tempfile.TemporaryDirectory() as temp:
                root=Path(temp)
                with sqlite3.connect(root/name) as db:
                    db.execute('create table tickets(id text)');db.execute("insert into tickets values ('preserved')")
                result=subprocess.run(['python3','-c',deploy.BACKUP.replace('/var/lib/machinist',temp),'release','{}'],capture_output=True,text=True)
                self.assertEqual(result.returncode,0,result.stderr)
                with sqlite3.connect(root/'deployments/release'/name) as backup:
                    self.assertEqual(backup.execute('select id from tickets').fetchone(),('preserved',))
                self.assertEqual([p.name for p in root.glob('*.db')],[name])

    def test_status_reads_tickets_and_active_runs(self):
        with tempfile.TemporaryDirectory() as temp:
            with sqlite3.connect(Path(temp)/'tickets.db') as db:
                db.execute('create table tickets(id text,state text)');db.execute("insert into tickets values ('t_1','done')")
                db.execute('create table runs(status text)');db.executemany('insert into runs values (?)',[('running',),('queued',),('succeeded',)])
            result=subprocess.run(['python3','-c',deploy.STATUS.replace('/var/lib/machinist',temp).replace('127.0.0.1:7341','127.0.0.1:1')],capture_output=True,text=True)
            self.assertEqual(result.returncode,0,result.stderr)
            self.assertEqual(json.loads(result.stdout),{'version':None,'tickets':{'t_1':'done'},'active_runs':2})

    def test_status_with_no_database_is_empty(self):
        with tempfile.TemporaryDirectory() as temp:
            result=subprocess.run(['python3','-c',deploy.STATUS.replace('/var/lib/machinist',temp).replace('127.0.0.1:7341','127.0.0.1:1')],capture_output=True,text=True)
            self.assertEqual(json.loads(result.stdout),{'version':None,'tickets':{},'active_runs':0})

    def test_runner_config_update_changes_only_the_image(self):
        updated=deploy.with_runner_image(RUNNER_CONFIG,IMAGE)
        self.assertIn('image = '+json.dumps(IMAGE),updated);self.assertIn('secret = "keep-me"',updated)
        with self.assertRaises(ValueError):deploy.with_runner_image('secret = "x"\n',IMAGE)

    def test_tags_and_malformed_revisions_never_reach_cluster(self):
        fake=FakeDeployment()
        for image,revision in [('registry/image:latest','a'*40),(IMAGE,'main')]:
            with self.assertRaises(ValueError):fake.run(image,IMAGE,revision,'unused')
        self.assertEqual(fake.calls,[])

if __name__=='__main__':unittest.main()
