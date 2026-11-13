"""Pure parsing and validation. Model output is data, never authority."""
import hashlib
import math
import re

REPOSITORY = re.compile(r'(?<![\w./-])([A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+)(?![\w/-])')


def extract_request(message, bot_username):
    if message.get('sender_chat') or message.get('from', {}).get('is_bot'):
        return None
    if not message.get('from', {}).get('id') or not message.get('reply_to_message'):
        return None
    text = message.get('text', message.get('caption', ''))
    entities = message.get('entities', message.get('caption_entities', []))
    addressed = False
    for entity in entities:
        # Telegram entity offsets count UTF-16 code units, not Python characters.
        encoded = text.encode('utf-16-le')
        start, length = entity.get('offset', 0), entity.get('length', 0)
        token = encoded[start * 2:(start + length) * 2].decode('utf-16-le', errors='replace').lower()
        if entity['type'] == 'mention' and token == '@' + bot_username.lower():
            addressed = True
        if entity['type'] == 'bot_command' and token in ('/issue', '/issue@' + bot_username.lower()):
            addressed = True
    if not addressed:
        return None
    source = message['reply_to_message']
    source_text = source.get('text', source.get('caption', ''))
    return {'chat_id': message['chat']['id'], 'chat_title': message['chat'].get('title', ''),
            'user_id': message['from']['id'], 'message_id': message['message_id'],
            'source_id': source['message_id'], 'instruction': text[:8000], 'source': source_text[:16000],
            'has_attachment': any(key in source for key in ('photo', 'document', 'video', 'voice', 'audio'))}


def choose_explicit_repository(instruction, allowed):
    names = {name.lower(): name for name in allowed}
    # Only command text controls explicit routing. Never trust a repository named in a quote.
    matches = {match.lower() for match in REPOSITORY.findall(instruction)}
    if not matches:
        return None
    if len(matches) != 1 or next(iter(matches)) not in names:
        raise ValueError('Укажите один разрешённый репозиторий в формате owner/repo.')
    return names[next(iter(matches))]


def validate_proposal(proposal, repositories, issues):
    if not isinstance(proposal, dict):
        raise ValueError('Invalid proposal')
    if proposal.get('repository') not in repositories:
        raise ValueError('Repository not allowed')
    confidence = proposal.get('confidence')
    if type(confidence) not in (int, float) or not math.isfinite(confidence) or not 0 <= confidence <= 1:
        raise ValueError('Invalid confidence')
    for field, limit in [('title', 200), ('body', 12000), ('question', 1000)]:
        value = proposal.get(field)
        if not isinstance(value, str) or len(value) > limit:
            raise ValueError('Invalid ' + field)
    if not proposal['title'].strip() or not proposal['body'].strip():
        raise ValueError('Empty issue')
    duplicate = proposal.get('duplicate_number')
    if duplicate is not None and (type(duplicate) is not int or duplicate not in {issue['number'] for issue in issues}):
        raise ValueError('Unknown duplicate')
    return proposal


def redact_secrets(text):
    patterns = [r'\bgh[pousr]_[A-Za-z0-9_]{20,}\b', r'\bgithub_pat_[A-Za-z0-9_]+',
                r'\bsk-[A-Za-z0-9_-]{20,}', r'\b\d{6,12}:[A-Za-z0-9_-]{25,}',
                r'(?i)\b(?:password|passwd|token|secret|api[_-]?key)\s*[=:]\s*[^\s,;]+',
                r'-----BEGIN [^-]*PRIVATE KEY-----[\s\S]*?-----END [^-]*PRIVATE KEY-----']
    for pattern in patterns:
        text = re.sub(pattern, '[REDACTED]', text)
    return text


def source_marker(chat_id, message_id):
    digest = hashlib.sha256(f'{chat_id}:{message_id}'.encode()).hexdigest()
    return f'issue-pilot-source:{digest}'
