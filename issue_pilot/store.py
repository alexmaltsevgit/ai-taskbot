"""SQLite state; a creating row survives crashes and prevents blind retries."""
import json
import os
import secrets
import sqlite3
import time


class Store:
    def __init__(self, path):
        path.parent.mkdir(parents=True, exist_ok=True)
        self.db = sqlite3.connect(path)
        os.chmod(path, 0o600)
        self.db.row_factory = sqlite3.Row
        self.db.executescript('''
            CREATE TABLE IF NOT EXISTS drafts (
              token TEXT PRIMARY KEY, source TEXT UNIQUE NOT NULL,
              user_id INTEGER NOT NULL, chat_id INTEGER NOT NULL,
              proposal TEXT NOT NULL, state TEXT NOT NULL, url TEXT, created REAL NOT NULL);
            CREATE TABLE IF NOT EXISTS settings (key TEXT PRIMARY KEY, value TEXT NOT NULL);
        ''')

    def get(self, token):
        row = self.db.execute('SELECT * FROM drafts WHERE token=?', (token,)).fetchone()
        return dict(row) if row else None

    def by_source(self, source):
        row = self.db.execute('SELECT * FROM drafts WHERE source=?', (source,)).fetchone()
        return dict(row) if row else None

    def save(self, source, user_id, chat_id, proposal):
        token = secrets.token_urlsafe(18)
        with self.db:
            self.db.execute('INSERT INTO drafts VALUES (?,?,?,?,?,?,?,?)',
                            (token, source, user_id, chat_id, json.dumps(proposal), 'preview', None, time.time()))
        return self.get(token)

    def transition(self, token, old, new, url=None):
        with self.db:
            result = self.db.execute('UPDATE drafts SET state=?,url=? WHERE token=? AND state=?',
                                     (new, url, token, old))
        return result.rowcount == 1

    def remove_replaceable(self, token):
        with self.db:
            self.db.execute("DELETE FROM drafts WHERE token=? AND state IN ('preview','cancelled','expired')", (token,))

    def setting(self, key, value=None):
        if value is not None:
            with self.db:
                self.db.execute('INSERT OR REPLACE INTO settings VALUES (?,?)', (key, str(value)))
            return str(value)
        row = self.db.execute('SELECT value FROM settings WHERE key=?', (key,)).fetchone()
        return row[0] if row else None

    def close(self):
        self.db.close()
