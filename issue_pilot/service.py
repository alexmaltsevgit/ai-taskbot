"""Orchestrates routing, duplicate detection and explicit confirmation."""
import hashlib
import json
import time
from .core import choose_explicit_repository, redact_secrets, source_marker, validate_proposal


class IssueService:
    def __init__(self, store, github, model, config):
        self.store, self.github, self.model, self.config = store, github, model, config

    def allowed(self, user_id, chat_id):
        policy = self.config.get(str(chat_id))
        return policy if policy and user_id in policy['users'] else None

    def result(self, row):
        state = 'uncertain' if row['state'] == 'creating' else row['state']
        return {'kind': state, 'token': row['token'], 'proposal': json.loads(row['proposal']), 'url': row['url']}

    def prepare(self, request):
        policy = self.allowed(request['user_id'], request['chat_id'])
        if not policy:
            return {'kind': 'denied'}
        marker = source_marker(request['chat_id'], request['source_id'])
        fingerprint = hashlib.sha256(request['instruction'].encode()).hexdigest()
        existing = self.store.by_source(marker)
        if existing:
            previous = json.loads(existing['proposal'])
            expired = existing['state'] == 'preview' and time.time() - existing['created'] > 86400
            changed = (existing['state'] == 'preview' and existing['user_id'] == request['user_id']
                       and previous.get('_request_fingerprint') != fingerprint)
            if expired or changed or existing['state'] in ('cancelled','expired'):
                self.store.remove_replaceable(existing['token'])
            else:
                return self.result(existing)
        if not request['source'].strip():
            return {'kind': 'question', 'text': 'Добавьте текстовое описание: вложения пока не анализируются.'}
        explicit = choose_explicit_repository(request['instruction'], policy['repositories'])
        repositories = self.github.repositories(policy['repositories'])
        allowed = [repository['full_name'] for repository in repositories]
        payload = {'instruction': redact_secrets(request['instruction']),
                   'quoted_message': redact_secrets(request['source']),
                   'chat_title': request['chat_title'], 'repositories': repositories,
                   'preferred_repository': explicit or policy.get('default_repository'),
                   'has_unread_attachment': request['has_attachment'], 'issues': []}
        routing = validate_proposal(self.model.propose(payload), allowed, [])
        if explicit and routing['repository'] != explicit:
            raise ValueError('Model ignored explicit repository')
        if routing['question'] or routing['confidence'] < .85:
            return {'kind':'question', 'text':routing['question'] or 'Укажите проект в формате owner/repo и повторите команду.'}
        repository = routing['repository']
        candidates = self.github.candidates(repository, routing['title'])
        for issue in candidates:
            if marker in (issue.get('body') or ''):
                return {'kind':'duplicate', 'url':issue['html_url']}
        payload['repositories'] = [r for r in repositories if r['full_name'] == repository]
        payload['preferred_repository'] = repository
        payload['issues'] = [{**issue, 'body':redact_secrets(issue.get('body') or '')[:4000]} for issue in candidates]
        proposal = validate_proposal(self.model.propose(payload), [repository], candidates)
        if proposal['question'] or proposal['confidence'] < .85:
            return {'kind':'question', 'text':proposal['question'] or 'Уточните содержание задачи.'}
        if proposal['duplicate_number']:
            issue = next(i for i in candidates if i['number'] == proposal['duplicate_number'])
            return {'kind':'duplicate', 'url':issue['html_url']}
        proposal['title'] = redact_secrets(proposal['title'])
        proposal['body'] = redact_secrets(proposal['body'])
        proposal['private'] = next(r['private'] for r in repositories if r['full_name'] == repository)
        proposal['_request_fingerprint'] = fingerprint
        row = self.store.save(marker, request['user_id'], request['chat_id'], proposal)
        return self.result(row)

    def confirm(self, token, user_id, chat_id):
        row = self.store.get(token)
        policy = self.allowed(user_id, chat_id)
        if not row or not policy or row['user_id'] != user_id or row['chat_id'] != chat_id:
            return {'kind':'denied'}
        if row['state'] != 'preview':
            return self.result(row)
        if time.time() - row['created'] > 86400:
            self.store.transition(token, 'preview', 'expired')
            return {'kind':'expired'}
        proposal = json.loads(row['proposal'])
        if proposal['repository'] not in policy['repositories']:
            return {'kind':'denied'}
        # Recheck visibility at action time: private -> public changes require a fresh preview.
        repository = self.github.repositories([proposal['repository']])[0]
        if repository['private'] != proposal['private']:
            self.store.transition(token, 'preview', 'expired')
            return {'kind':'expired'}
        for issue in self.github.candidates(proposal['repository'], proposal['title']):
            if row['source'] in (issue.get('body') or ''):
                self.store.transition(token, 'preview', 'created', issue['html_url'])
                return self.result(self.store.get(token))
        if not self.store.transition(token, 'preview', 'creating'):
            return self.result(self.store.get(token))
        body = proposal['body'] + '\n\n<!-- ' + row['source'] + ' -->'
        try:
            created = self.github.create(proposal['repository'], proposal['title'], body)
            self.store.transition(token, 'creating', 'created', created['html_url'])
        except Exception:
            # A timeout can happen AFTER GitHub creates the issue. Never submit a second POST.
            self.store.transition(token, 'creating', 'uncertain')
        return self.result(self.store.get(token))

    def cancel(self, token, user_id, chat_id):
        row = self.store.get(token)
        if not row or row['user_id'] != user_id or row['chat_id'] != chat_id or not self.allowed(user_id, chat_id):
            return {'kind':'denied'}
        self.store.transition(token, 'preview', 'cancelled')
        return self.result(self.store.get(token))
