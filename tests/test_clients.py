import unittest
from issue_pilot.clients import GithubClient, ModelClient
from issue_pilot.worker import codex_command

class ClientTests(unittest.TestCase):
    def test_search_query_cannot_inject_repository_qualifiers(self):
        client=GithubClient(lambda:'token'); calls=[]
        def request(method,path,data=None):
            calls.append(path)
            return {'items':[], 'incomplete_results':False} if path.startswith('/search') else []
        client.request=request
        client.candidates('acme/paint','bug repo:evil/private is:pr')
        self.assertTrue(any('repo%3Aacme%2Fpaint' in p for p in calls))
        self.assertFalse(any('repo%3Aevil' in p for p in calls))
    def test_incomplete_search_stops_creation(self):
        client=GithubClient(lambda:'token')
        client.request=lambda *args,**kwargs: {'items':[], 'incomplete_results':True}
        with self.assertRaises(RuntimeError): client.candidates('acme/paint','bug')
    def test_codex_is_isolated_and_prompt_is_not_a_shell_argument(self):
        command=codex_command('/tmp/work','/tmp/schema','/tmp/output',None)
        self.assertIn('--ignore-user-config',command)
        self.assertIn('read-only',command)
        self.assertIn('shell_tool',command)
        self.assertEqual(command[-1],'-')
        self.assertNotIn('--dangerously-bypass-approvals-and-sandbox',command)
