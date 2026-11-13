import unittest
from issue_pilot.bot import render_result, validate_config

class BotTests(unittest.TestCase):
    def test_preview_contains_whole_body_and_public_warning(self):
        result={'kind':'preview','token':'abc','proposal':{'repository':'acme/paint','title':'Fix','body':'x'*6000,'private':False}}
        messages=render_result(result)
        self.assertTrue(any('ПУБЛИЧНЫЙ' in text for text,buttons in messages))
        self.assertEqual(''.join(text for text,buttons in messages).count('x'),6001)
        self.assertTrue(messages[-1][1])
        self.assertTrue(all(len(text)<=3500 for text,buttons in messages))
    def test_invalid_or_empty_allowlist_rejected(self):
        for config in ({}, {'1':{'users':[],'repositories':['acme/paint']}}, {'1':{'users':[1],'repositories':['../evil']}}):
            with self.assertRaises(ValueError): validate_config(config)
    def test_valid_config(self):
        config={'-1001':{'users':[7],'repositories':['acme/paint']}}
        self.assertEqual(validate_config(config),config)

    def test_stale_callback_ack_does_not_drop_confirmation(self):
        from issue_pilot.bot import handle_update
        from unittest.mock import Mock
        service=Mock(); service.confirm.return_value={'kind':'created','url':'https://github.com/a/b/issues/1'}
        telegram=Mock()
        def call(method,**kwargs):
            if method=='answerCallbackQuery': raise RuntimeError('QUERY_ID_INVALID')
        telegram.call.side_effect=call
        update={'callback_query':{'id':'old','from':{'id':7},'data':'create:token','message':{'message_id':8,'chat':{'id':-1}}}}
        handle_update(update,service,telegram,'bot')
        service.confirm.assert_called_once_with('token',7,-1)
