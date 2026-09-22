import unittest
import json, os, tempfile
from pathlib import Path
from unittest.mock import patch
from gate import run
from gate import auto_eligible, validate

class PolicyTests(unittest.TestCase):
    def test_only_small_low_risk_docs_auto_merge(self):
        self.assertTrue(auto_eligible('low', ['docs/guide.md'], 10, 2))
        for risk, paths, count in [('medium',['docs/a.md'],1),('high',['README.md'],1),('low',['.github/workflows/release.yml'],1),('low',['internal/auth.go'],1),('low',['docs/a.py'],1),('low',['README.md'],201),('low',[],1)]:
            self.assertFalse(auto_eligible(risk,paths,count,0))
    def test_changed_head_or_invalid_risk_blocks(self):
        pr={'head_sha':'a'*40}
        for risk, head in [('low','b'*40),('unknown','a'*40)]:
            with self.assertRaises(ValueError):
                validate(pr,{'head_sha':'a'*40,'risk':risk,'reason':'test'},head)
        validate(pr,{'head_sha':'a'*40,'risk':'low','reason':'docs'},'a'*40)

class MergeTests(unittest.TestCase):
    def test_missing_checks_never_calls_merge(self):
        with tempfile.TemporaryDirectory() as tmp:
            pr = {'url':'https://github.com/owainlewis/machinist/pull/1','head_sha':'a'*40}
            risk = {'head_sha':'a'*40,'risk':'low','reason':'docs'}
            values = {'pr':pr,'risk':risk,'decision':{'pr':pr,'assessment':risk,'automatic':True,'base_sha':'b'*40}}
            inputs = {}
            for key,value in values.items():
                path=Path(tmp,key+'.json'); path.write_text(json.dumps(value)); inputs[key]=str(path)
            remote={'state':'open','draft':False,'base':{'ref':'main','sha':'b'*40},'head':{'sha':'a'*40,'repo':{'full_name':'owainlewis/machinist'}},'mergeable':True,'mergeable_state':'clean'}
            with patch('sys.argv',['gate.py','merge','owainlewis/machinist']), patch('gate.gh',side_effect=[remote,{'reviewDecision':'','statusCheckRollup':[],'headRefOid':'a'*40,'baseRefOid':'b'*40}]), patch('gate.subprocess.run') as merge:
                with self.assertRaisesRegex(ValueError,'checks'):
                    run('merge',inputs)
                merge.assert_not_called()


class PolicyIntegrationTests(unittest.TestCase):
    def setUp(self):
        self.directory = tempfile.TemporaryDirectory()
        self.addCleanup(self.directory.cleanup)
        self.root = Path(self.directory.name)
        self.pr = {'url':'https://github.com/owainlewis/machinist/pull/1','head_sha':'a'*40}
        self.risk = {'head_sha':'a'*40,'risk':'low','reason':'docs only'}
        self.data = {'state':'open','draft':False,'base':{'ref':'main','sha':'b'*40},'head':{'sha':'a'*40,'repo':{'full_name':'owainlewis/machinist'}},'mergeable':True,'mergeable_state':'clean','changed_files':1,'additions':2,'deletions':1}
        self.checks = {'headRefOid':'a'*40,'baseRefOid':'b'*40,'reviewDecision':'APPROVED','statusCheckRollup':[{'__typename':'CheckRun','status':'COMPLETED','conclusion':'SUCCESS'}]}
        self.threads = {'data':{'repository':{'pullRequest':{'reviewThreads':{'nodes':[],'pageInfo':{'hasNextPage':False}}}}}}
        self.inputs = {}
        for key, value in {'pr':self.pr,'risk':self.risk,'decision':{'pr':self.pr,'assessment':self.risk,'automatic':True,'base_sha':'b'*40}}.items():
            path=self.root/(key+'.json'); path.write_text(json.dumps(value)); self.inputs[key]=str(path)
        self.argv = patch('sys.argv',['gate.py','merge','owainlewis/machinist']); self.argv.start(); self.addCleanup(self.argv.stop)
        self.env = patch.dict(os.environ, {'MACHINIST_OUTPUT_DIR':str(self.root)}); self.env.start(); self.addCleanup(self.env.stop)

    def test_success_merges_only_reviewed_sha_without_bypass(self):
        with patch('gate.gh',side_effect=[self.data,self.checks,self.threads,{'merged':True}]), patch('gate.subprocess.run') as merge:
            self.assertEqual(run('merge',self.inputs)['outcome'],'complete')
            self.assertEqual(merge.call_args.args[0], ['gh','pr','merge',self.pr['url'],'--squash','--match-head-commit','a'*40])

    def test_failed_pending_missing_and_required_reviews_block(self):
        for checks in [[],[{'__typename':'CheckRun','status':'COMPLETED','conclusion':'FAILURE'}],[{'__typename':'CheckRun','status':'IN_PROGRESS','conclusion':None}],[{'__typename':'StatusContext','state':'PENDING'}]]:
            self.checks['statusCheckRollup']=checks
            with patch('gate.gh',side_effect=[self.data,self.checks]), patch('gate.subprocess.run') as merge:
                with self.assertRaises(ValueError): run('merge',self.inputs)
                merge.assert_not_called()

    def test_changed_base_blocks(self):
        self.data['base']['sha']='c'*40
        with patch('gate.gh',return_value=self.data), patch('gate.subprocess.run') as merge:
            with self.assertRaisesRegex(ValueError,'Base changed'): run('merge',self.inputs)
            merge.assert_not_called()

    def test_unresolved_threads_block(self):
        self.threads['data']['repository']['pullRequest']['reviewThreads']['nodes']=[{'isResolved':False}]
        with patch('gate.gh',side_effect=[self.data,self.checks,self.threads]), patch('gate.subprocess.run') as merge:
            with self.assertRaisesRegex(ValueError,'review threads'): run('merge',self.inputs)
            merge.assert_not_called()

    def test_successful_merge_retry_is_idempotent(self):
        self.data.update(state='closed',merged=True)
        with patch('gate.gh',return_value=self.data), patch('gate.subprocess.run') as merge:
            self.assertEqual(run('merge',self.inputs)['outcome'],'complete')
            merge.assert_not_called()

    def test_gate_medium_and_code_changes_require_approval(self):
        for risk, filename, mode, expected in [('low','docs/guide.md','100644',False),('medium','docs/guide.md','100644',True),('low','internal/auth.go','100644',True),('low','docs/guide.md','120000',True)]:
            self.risk['risk']=risk
            Path(self.inputs['risk']).write_text(json.dumps(self.risk))
            with patch('gate.gh',side_effect=[self.data,[[{'filename':filename,'status':'modified'}]],{'tree':[{'path':filename,'mode':mode}]}]), patch('gate.subprocess.run') as merge:
                self.assertEqual(run('gate',self.inputs)['approval_required'],expected)
                merge.assert_not_called()

if __name__ == '__main__': unittest.main()
