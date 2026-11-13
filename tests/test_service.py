import tempfile
import unittest
from pathlib import Path
from issue_pilot.service import IssueService
from issue_pilot.store import Store

class Github:
    def __init__(self): self.created=[]; self.issues=[]; self.fail=False
    def repositories(self, names): return [{'full_name':name,'description':'paint project','private':True} for name in names]
    def candidates(self, repository, text): return self.issues
    def create(self, repository, title, body):
        if self.fail: raise TimeoutError('uncertain')
        self.created.append((repository,title,body)); return {'html_url':'https://github.com/acme/paint/issues/1'}

class Model:
    def propose(self, payload):
        return {'repository':'acme/paint','confidence':.99,'title':'Fix save','body':'## Expected\nSave works','duplicate_number':None,'question':''}

class ServiceTests(unittest.TestCase):
    def setUp(self):
        self.tmp=tempfile.TemporaryDirectory(); self.store=Store(Path(self.tmp.name)/'db.sqlite')
        self.github=Github(); self.service=IssueService(self.store,self.github,Model(),{'-1001':{'users':[7],'repositories':['acme/paint']}})
        self.request={'chat_id':-1001,'user_id':7,'message_id':8,'source_id':3,'instruction':'create issue','source':'save fails','chat_title':'Paint','has_attachment':False}
    def tearDown(self): self.store.close(); self.tmp.cleanup()
    def test_preview_then_create(self):
        result=self.service.prepare(self.request)
        self.assertEqual(result['kind'],'preview'); self.assertEqual(self.github.created,[])
        result=self.service.confirm(result['token'],7,-1001)
        self.assertEqual(result['kind'],'created'); self.assertEqual(len(self.github.created),1)
    def test_confirmation_is_bound_to_user_and_chat(self):
        token=self.service.prepare(self.request)['token']
        for user,chat in [(8,-1001),(7,-1002)]:
            self.assertEqual(self.service.confirm(token,user,chat)['kind'],'denied')
        self.assertEqual(self.github.created,[])
    def test_duplicate_delivery_and_confirm_are_idempotent(self):
        token=self.service.prepare(self.request)['token']
        self.assertEqual(self.service.prepare(self.request)['token'],token)
        self.service.confirm(token,7,-1001); self.service.confirm(token,7,-1001)
        self.assertEqual(len(self.github.created),1)
    def test_unknown_user_never_reaches_model_or_github(self):
        self.request['user_id']=99
        self.assertEqual(self.service.prepare(self.request)['kind'],'denied')
    def test_uncertain_create_is_not_retried(self):
        token=self.service.prepare(self.request)['token']; self.github.fail=True
        self.assertEqual(self.service.confirm(token,7,-1001)['kind'],'uncertain')
        self.github.fail=False
        self.assertEqual(self.service.confirm(token,7,-1001)['kind'],'uncertain')
        self.assertEqual(self.github.created,[])
    def test_cancel_never_creates(self):
        token=self.service.prepare(self.request)['token']
        self.service.cancel(token,7,-1001)
        self.assertEqual(self.service.confirm(token,7,-1001)['kind'],'cancelled')
    def test_source_already_created_returns_link(self):
        token=self.service.prepare(self.request)['token']; self.service.confirm(token,7,-1001)
        self.request['message_id']=9
        self.assertEqual(self.service.prepare(self.request)['kind'],'created')
    def test_existing_duplicate_skips_creation(self):
        self.github.issues=[{'number':4,'title':'Save fails','body':'','html_url':'https://github.com/acme/paint/issues/4'}]
        self.service.model.propose=lambda payload: {'repository':'acme/paint','confidence':1,'title':'Fix save','body':'Fix','question':'','duplicate_number':4 if payload['issues'] else None}
        self.assertEqual(self.service.prepare(self.request)['kind'],'duplicate')
        self.assertEqual(self.github.created,[])

    def test_null_body_candidate_is_supported(self):
        self.github.issues=[{'number':2,'title':'Other','body':None,'html_url':'https://github.com/acme/paint/issues/2'}]
        self.assertEqual(self.service.prepare(self.request)['kind'],'preview')

    def test_cancelled_draft_can_be_replaced_old_token_is_invalid(self):
        old=self.service.prepare(self.request)['token']; self.service.cancel(old,7,-1001)
        new=self.service.prepare(self.request)['token']
        self.assertNotEqual(old,new)
        self.assertEqual(self.service.confirm(old,7,-1001)['kind'],'denied')

    def test_changed_instruction_replaces_preview(self):
        old=self.service.prepare(self.request)['token']
        self.request['instruction']='create issue, include reproduction steps'
        new=self.service.prepare(self.request)['token']
        self.assertNotEqual(old,new)
        self.assertEqual(self.service.confirm(old,7,-1001)['kind'],'denied')
