"""GitHub App installation tokens preferred; narrowly scoped PAT is optional."""
import base64
import json
import os
import subprocess
import time
from .clients import request_json


def encode(value):
    return base64.urlsafe_b64encode(value).rstrip(b'=')


def token_provider():
    if token := os.environ.get('GITHUB_TOKEN'):
        return lambda: token
    app_id = os.environ['GITHUB_APP_ID']
    installation = os.environ['GITHUB_INSTALLATION_ID']
    private_key = os.environ['GITHUB_PRIVATE_KEY_FILE']
    cached = {'token':None,'until':0}

    def get_token():
        if time.time() < cached['until']:
            return cached['token']
        now = int(time.time())
        header = encode(json.dumps({'alg':'RS256','typ':'JWT'}).encode())
        claims = encode(json.dumps({'iat':now-60,'exp':now+540,'iss':app_id}).encode())
        message = header+b'.'+claims
        signature = subprocess.run(['openssl','dgst','-sha256','-sign',private_key],input=message,
                                   capture_output=True,check=True,timeout=10).stdout
        jwt = (message+b'.'+encode(signature)).decode()
        result = request_json(f'https://api.github.com/app/installations/{int(installation)}/access_tokens',
                              'POST', {}, {'Authorization':'Bearer '+jwt,'Accept':'application/vnd.github+json',
                                           'User-Agent':'telegram-issue-pilot'})
        cached.update(token=result['token'],until=time.time()+3000)
        return cached['token']
    return get_token
