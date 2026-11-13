"""Small HTTP adapters. No request bodies or credentials are logged."""
import json
import re
import urllib.parse
import urllib.request


def request_json(url, method='GET', data=None, headers=None, timeout=45):
    body = None if data is None else json.dumps(data).encode()
    request = urllib.request.Request(url, data=body, method=method,
                                     headers={'Content-Type':'application/json', **(headers or {})})
    with urllib.request.urlopen(request, timeout=timeout) as response:
        raw = response.read(4_000_001)
        if len(raw) > 4_000_000:
            raise RuntimeError('Response exceeds safe size')
        return json.loads(raw)


class GithubClient:
    def __init__(self, token):
        self.token = token

    def request(self, method, path, data=None):
        return request_json('https://api.github.com' + path, method, data,
                            {'Authorization':'Bearer ' + self.token(), 'Accept':'application/vnd.github+json',
                             'X-GitHub-Api-Version':'2022-11-28', 'User-Agent':'telegram-issue-pilot'})

    def repositories(self, names):
        result = []
        for name in names:
            repo = self.request('GET', '/repos/' + name)
            if repo.get('archived') or not repo.get('has_issues'):
                raise ValueError('Repository unavailable for issues')
            result.append({'full_name':repo['full_name'], 'description':repo.get('description') or '',
                           'private':repo['private']})
        return result

    def candidates(self, repository, text):
        # Terms are quoted Unicode words; caller/model cannot inject GitHub search qualifiers.
        words = [w for w in re.findall(r'[^\W_]{3,}', text, re.UNICODE) if w.lower() not in {'repo','is','https'}][:6]
        query = f'repo:{repository} is:issue ' + ' '.join('"' + word + '"' for word in words[:3])
        found = self.request('GET', '/search/issues?' + urllib.parse.urlencode({'q':query,'per_page':50}))
        if found.get('incomplete_results'):
            raise RuntimeError('GitHub duplicate search incomplete; retry later')
        # Search is lexical and indexed with a delay. Include recent open and closed issues too.
        recent = self.request('GET', f'/repos/{repository}/issues?state=all&sort=updated&direction=desc&per_page=100')
        issues = {}
        for item in [*found['items'], *recent]:
            if 'pull_request' not in item:
                issues[item['number']] = {key:item.get(key) for key in ('number','title','body','html_url','state')}
        return list(issues.values())[:150]

    def create(self, repository, title, body):
        return self.request('POST', f'/repos/{repository}/issues', {'title':title,'body':body})


class TelegramClient:
    def __init__(self, token):
        self.base = 'https://api.telegram.org/bot' + token + '/'

    def call(self, method, **data):
        result = request_json(self.base + method, 'POST', data, timeout=50)
        if not result.get('ok'):
            raise RuntimeError('Telegram request failed')
        return result['result']


class ModelClient:
    def __init__(self, endpoint, token):
        self.endpoint, self.token = endpoint, token

    def propose(self, payload):
        return request_json(self.endpoint + '/propose', 'POST', payload,
                            {'Authorization':'Bearer ' + self.token}, timeout=200)
