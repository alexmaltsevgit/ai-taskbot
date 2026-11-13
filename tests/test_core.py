import unittest
from issue_pilot.core import extract_request, choose_explicit_repository, validate_proposal, redact_secrets, source_marker

class CoreTests(unittest.TestCase):
    def message(self, text='@pilot_bot создай задачу'):
        return {'message_id': 8, 'from': {'id': 7}, 'chat': {'id': -1001, 'title': 'Paint'}, 'text': text,
                'entities': [{'type':'mention','offset':0,'length':10}],
                'reply_to_message': {'message_id': 3, 'text': 'The save button fails'}}

    def test_reply_and_exact_mention_are_required(self):
        self.assertIsNotNone(extract_request(self.message(), 'pilot_bot'))
        self.assertIsNone(extract_request(self.message('@other_bot issue'), 'pilot_bot'))
        msg=self.message(); del msg['reply_to_message']
        self.assertIsNone(extract_request(msg,'pilot_bot'))

    def test_command_and_utf16_mention(self):
        msg=self.message('/issue@pilot_bot fix it'); msg['entities']=[{'type':'bot_command','offset':0,'length':16}]
        self.assertIsNotNone(extract_request(msg,'pilot_bot'))
        msg=self.message('😀 @pilot_bot add'); msg['entities']=[{'type':'mention','offset':3,'length':10}]
        self.assertIsNotNone(extract_request(msg,'pilot_bot'))

    def test_rejects_bot_and_anonymous_sender(self):
        msg=self.message(); msg['sender_chat']={'id':-1}
        self.assertIsNone(extract_request(msg,'pilot_bot'))
        msg=self.message(); msg['from']['is_bot']=True
        self.assertIsNone(extract_request(msg,'pilot_bot'))

    def test_explicit_repo_must_be_allowlisted(self):
        self.assertEqual(choose_explicit_repository('use acme/paint', ['acme/paint']), 'acme/paint')
        with self.assertRaises(ValueError): choose_explicit_repository('use other/private', ['acme/paint'])
        with self.assertRaises(ValueError): choose_explicit_repository('acme/paint acme/api', ['acme/paint','acme/api'])

    def test_proposal_cannot_invent_repo_or_duplicate(self):
        proposal={'repository':'acme/paint','confidence':.99,'title':'Fix save','body':'Expected: save succeeds','duplicate_number':None,'question':''}
        self.assertEqual(validate_proposal(proposal,['acme/paint'],[]),proposal)
        with self.assertRaises(ValueError): validate_proposal(proposal,['acme/api'],[])
        proposal['duplicate_number']=999
        with self.assertRaises(ValueError): validate_proposal(proposal,['acme/paint'],[{'number':1}])

    def test_secrets_redacted(self):
        text='token ghp_'+'a'*36+' password=hunter2 user@example.org'
        result=redact_secrets(text)
        self.assertNotIn('hunter2',result); self.assertNotIn('ghp_',result)
        self.assertIn('user@example.org',result)

    def test_source_marker_does_not_expose_chat(self):
        self.assertEqual(source_marker(-1001,3), source_marker(-1001,3))
        self.assertNotIn('-1001',source_marker(-1001,3))
