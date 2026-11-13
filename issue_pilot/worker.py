"""Private inference service. Never mount GitHub/Telegram credentials here."""
import hmac
import json
import os
from pathlib import Path
import subprocess
import tempfile
from http.server import BaseHTTPRequestHandler, HTTPServer
from .prompts import SYSTEM_PROMPT, PROPOSAL_SCHEMA


def codex_command(workdir, schema, output, model):
    command = ['codex', 'exec', '--ignore-user-config', '--ignore-rules', '--ephemeral',
               '--skip-git-repo-check', '--sandbox', 'read-only', '--disable', 'shell_tool',
               '--disable', 'apps', '--disable', 'multi_agent', '-c', 'web_search="disabled"',
               '--cd', workdir, '--output-schema', schema, '--output-last-message', output]
    if model:
        command += ['--model', model]
    return [*command, '-']


def infer(payload):
    with tempfile.TemporaryDirectory(prefix='issue-pilot-') as directory:
        schema, output = Path(directory)/'schema.json', Path(directory)/'result.json'
        schema.write_text(json.dumps(PROPOSAL_SCHEMA))
        prompt = SYSTEM_PROMPT + '\nUNTRUSTED INPUT JSON:\n' + json.dumps(payload, ensure_ascii=False)
        # Do not pass the worker HTTP token or application credentials to the child.
        environment = {key:os.environ[key] for key in ('PATH','HOME','CODEX_HOME','LANG','SSL_CERT_FILE') if key in os.environ}
        completed = subprocess.run(codex_command(directory,str(schema),str(output),os.getenv('CODEX_MODEL')),
                                   input=prompt, text=True, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL,
                                   timeout=180, env=environment, check=False)
        if completed.returncode != 0 or not output.exists() or output.stat().st_size > 65536:
            raise RuntimeError('Codex invocation failed')
        return json.loads(output.read_text())


class Handler(BaseHTTPRequestHandler):
    def setup(self):
        super().setup()
        self.connection.settimeout(15)

    def log_message(self, *_):
        pass

    def do_POST(self):
        expected = 'Bearer ' + os.environ['WORKER_TOKEN']
        if self.path != '/propose' or not hmac.compare_digest(self.headers.get('Authorization',''), expected):
            self.send_error(403)
            return
        try:
            length = int(self.headers.get('Content-Length','0'))
            if not 0 < length <= 1_000_000:
                raise ValueError('Invalid size')
            payload = json.loads(self.rfile.read(length))
            result = infer(payload)
            encoded = json.dumps(result).encode()
            self.send_response(200); self.send_header('Content-Type','application/json')
            self.send_header('Content-Length', str(len(encoded))); self.end_headers(); self.wfile.write(encoded)
        except Exception:
            self.send_error(503, 'Inference unavailable; check login and subscription limits')


def main():
    if len(os.environ.get('WORKER_TOKEN','')) < 32:
        raise ValueError('WORKER_TOKEN must contain at least 32 characters')
    HTTPServer(('0.0.0.0',8090), Handler).serve_forever()


if __name__ == '__main__':
    main()
