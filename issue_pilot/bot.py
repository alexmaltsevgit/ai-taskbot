"""Single-instance polling runner. Long model calls are serialized deliberately."""
import fcntl
import json
import logging
import os
from pathlib import Path
import re
import time
from .clients import GithubClient, ModelClient, TelegramClient
from .core import extract_request
from .service import IssueService
from .store import Store


def validate_config(config):
    if not isinstance(config, dict) or not config:
        raise ValueError('Configure at least one chat')
    for chat_id, policy in config.items():
        if not re.fullmatch(r'-?\d+', chat_id) or not isinstance(policy, dict):
            raise ValueError('Invalid chat policy')
        users, repos = policy.get('users'), policy.get('repositories')
        if not isinstance(users, list) or not users or any(type(user) is not int or user <= 0 for user in users):
            raise ValueError('Specify numeric Telegram user IDs')
        if not isinstance(repos, list) or not repos or any(not isinstance(repo,str) or not re.fullmatch(r'[A-Za-z0-9][A-Za-z0-9_-]*/[A-Za-z0-9][A-Za-z0-9_.-]*', repo) for repo in repos):
            raise ValueError('Invalid repository allowlist')
        if policy.get('default_repository') and policy['default_repository'] not in repos:
            raise ValueError('Default repository must be allowed')
    return config


def render_result(result):
    kind = result['kind']
    buttons = None
    if kind == 'preview':
        proposal = result['proposal']
        visibility = 'приватный' if proposal['private'] else 'ПУБЛИЧНЫЙ, содержимое будет видно всем'
        text = f"Репозиторий: {proposal['repository']} ({visibility})\n\n{proposal['title']}\n\n{proposal['body']}"
        buttons = {'inline_keyboard':[[{'text':'Создать issue','callback_data':'create:' + result['token']},
                                       {'text':'Отмена','callback_data':'cancel:' + result['token']}]]}
    elif kind in ('created','duplicate'):
        text = ('Создано: ' if kind == 'created' else 'Нашёл существующую задачу: ') + result['url']
    elif kind == 'question':
        text = result['text']
    else:
        text = {'denied':'Нет доступа к этой операции.', 'cancelled':'Создание отменено.',
                'expired':'Черновик устарел. Повторите команду ответом на исходное сообщение.',
                'uncertain':'Результат создания не подтверждён. Я не повторяю запрос, чтобы не создать дубль. Проверьте issues и попросите администратора сверить запись.'}.get(kind,'Операция не завершена.')
    chunks = [(text[i:i+3500], None) for i in range(0,len(text),3500)]
    chunks[-1] = (chunks[-1][0], buttons)
    return chunks


def send_result(telegram, chat_id, message_id, result):
    for text, keyboard in render_result(result):
        args = {'chat_id':chat_id,'text':text,'link_preview_options':{'is_disabled':True},
                'reply_parameters':{'message_id':message_id,'allow_sending_without_reply':True}}
        if keyboard:
            args['reply_markup'] = keyboard
        telegram.call('sendMessage', **args)


def handle_update(update, service, telegram, username):
    if callback := update.get('callback_query'):
        message = callback.get('message') or {}
        chat_id = message.get('chat',{}).get('id')
        user_id = callback.get('from',{}).get('id')
        data = callback.get('data','')
        if not chat_id or not data.startswith(('create:','cancel:')):
            return
        # Acknowledge before potentially slow GitHub calls; this does not mean success.
        try:
            telegram.call('answerCallbackQuery', callback_query_id=callback['id'])
        except Exception:
            logging.info('Callback acknowledgment unavailable; processing durable confirmation')
        action, token = data.split(':',1)
        result = (service.confirm if action == 'create' else service.cancel)(token,user_id,chat_id)
        send_result(telegram,chat_id,message['message_id'],result)
        return
    message = update.get('message')
    if not message:
        return
    request = extract_request(message,username)
    if not request or not service.allowed(request['user_id'],request['chat_id']):
        return
    telegram.call('sendMessage', chat_id=request['chat_id'], text='Проверю проект и похожие задачи.',
                  reply_parameters={'message_id':request['message_id']})
    try:
        result = service.prepare(request)
    except ValueError:
        result = {'kind':'question','text':'Не удалось однозначно подготовить задачу. Укажите разрешённый owner/repo и уточните описание.'}
    except Exception:
        # Never print exception objects: HTTP exceptions may contain credentials in URLs.
        logging.warning('Preparation failed; no issue creation was attempted')
        result = {'kind':'question','text':'Не удалось проверить GitHub или получить ответ модели. Issue не создавал. Повторите команду позже.'}
    send_result(telegram,request['chat_id'],request['message_id'],result)


def main():
    os.umask(0o077)
    logging.basicConfig(level=logging.INFO,format='%(asctime)s %(levelname)s %(message)s')
    config = validate_config(json.loads(Path(os.getenv('CONFIG_FILE','config.json')).read_text()))
    state = Path(os.getenv('STATE_FILE','data/state.sqlite'))
    state.parent.mkdir(parents=True,exist_ok=True)
    # One poller owns this database. SQLite still atomically claims create operations.
    lock = open(str(state)+'.lock','a')
    fcntl.flock(lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
    store = Store(state)
    telegram = TelegramClient(os.environ['TELEGRAM_BOT_TOKEN'])
    from .github_auth import token_provider
    service = IssueService(store,GithubClient(token_provider()),
                           ModelClient(os.getenv('WORKER_URL','http://worker:8090'),os.environ['WORKER_TOKEN']),config)
    username = telegram.call('getMe')['username']
    while True:
        try:
            updates = telegram.call('getUpdates',offset=int(store.setting('offset') or 0),timeout=30,
                                    allowed_updates=['message','callback_query'])
            for update in updates:
                try:
                    handle_update(update,service,telegram,username)
                except Exception:
                    logging.warning('Update failed or delivery uncertain; update_id=%s',update['update_id'])
                # Do not blindly resend uncertain Telegram writes. Draft/result is durable.
                store.setting('offset',update['update_id']+1)
        except Exception:
            logging.warning('Polling unavailable; retrying in 5 seconds')
            time.sleep(5)


if __name__ == '__main__':
    main()
