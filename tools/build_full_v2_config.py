#!/usr/bin/env python3
import json
from copy import deepcopy
from pathlib import Path
import yaml

ROOT = Path(__file__).resolve().parents[1]
RAW = ROOT / '.codex-tasks/20260316-config-dir-v2/raw/legacy_merged_config.json'
OUT = ROOT / 'config/config.yaml'
AUDIT_SETTINGS = ROOT / 'config/audit_settings.json'

STATE_BASE = 'state'
WEBINFO_MAP = {
    'clientname': 'webinfo/clientname.json',
}
REQUERY_MAP = {
    'requery': 'webinfo/requeryconfig.json',
}
SWITCH_STATE_FILE = 'switches.json'


def read_json(path: Path):
    return json.loads(path.read_text())


def normalized_audit():
    default = {
        'memory_entries': 100000,
        'retention_days': 30,
        'max_disk_size_mb': 10,
        'max_db_size_mb': 10,
        'storage_engine': 'sqlite',
        'sqlite_path': 'db/audit.db',
    }
    if AUDIT_SETTINGS.exists():
        current = read_json(AUDIT_SETTINGS)
        default.update({
            'memory_entries': current.get('memory_entries', default['memory_entries']),
            'retention_days': current.get('retention_days', default['retention_days']),
            'max_disk_size_mb': current.get('max_disk_size_mb', default['max_disk_size_mb']),
            'max_db_size_mb': current.get('max_db_size_mb', default['max_db_size_mb']),
        })
    return default


def remap_plugin(plugin):
    plugin = deepcopy(plugin)
    tag = plugin.get('tag', '')
    typ = plugin.get('type', '')
    args = plugin.get('args') or {}
    if typ == 'webinfo':
        args['file'] = f"{STATE_BASE}/{WEBINFO_MAP.get(tag, f'webinfo/{tag}.json')}"
    elif typ == 'requery':
        args['file'] = f"{STATE_BASE}/{REQUERY_MAP.get(tag, f'requery/{tag}.json')}"
    elif typ == 'switch':
        args['state_file_path'] = f"{STATE_BASE}/{SWITCH_STATE_FILE}"
    elif typ == 'cache' and args.get('dump_file'):
        dump_file = str(args['dump_file']).strip()
        if dump_file.startswith('cache/'):
            args['dump_file'] = 'db/' + dump_file
    plugin['args'] = args
    plugin.pop('_file', None)
    return plugin


def build():
    merged = read_json(RAW)
    plugins = [remap_plugin(p) for p in merged['plugins']]
    doc = {
        'version': 'v2',
        'log': merged.get('log') or {'level': 'warn'},
        'api': merged.get('api') or {},
        'audit': normalized_audit(),
        'storage': {'control_db': 'db/control.db'},
        'policies': [
            {
                'name': p['tag'],
                'type': p['type'],
                'args': p.get('args'),
            }
            for p in plugins
        ],
    }
    OUT.write_text(yaml.safe_dump(doc, allow_unicode=True, sort_keys=False, width=120))


if __name__ == '__main__':
    build()
