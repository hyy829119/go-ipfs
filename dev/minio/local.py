#!/usr/bin/env python3
"""Build official MinIO source and run a loopback-only development container."""
import argparse
import json
import os
from pathlib import Path
import platform
import secrets
import shlex
import shutil
import socket
import subprocess
import urllib.error
import urllib.request
import time

ROOT = Path(__file__).resolve().parents[2]
HERE = Path(__file__).resolve().parent
STATE = HERE / '.local'
VERSION = 'v0.0.0-20260212201848-7aac2a2c5b7c'
NAME = 'ifshare-minio'
VOLUME = 'ifshare-minio-data'


def run(args, **kwargs):
    return subprocess.run([str(x) for x in args], check=True, **kwargs)


def private_file(path, content):
    fd = os.open(path, os.O_WRONLY | os.O_CREAT | os.O_TRUNC, 0o600)
    with os.fdopen(fd, 'w') as stream:
        stream.write(content)
    path.chmod(0o600)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('action', choices=['start', 'stop'], nargs='?', default='start')
    parser.add_argument('--port', type=int, default=19000)
    parser.add_argument('--console-port', type=int, default=19001)
    args = parser.parse_args()
    if args.action == 'stop':
        run(['docker', 'stop', NAME])
        print('MinIO stopped; volume and credentials retained.')
        return
    if args.port == args.console_port or not all(1 <= p <= 65535 for p in (args.port, args.console_port)):
        parser.error('ports must be distinct and between 1 and 65535')
    arch = {'arm64': 'arm64', 'aarch64': 'arm64', 'x86_64': 'amd64', 'AMD64': 'amd64'}.get(platform.machine())
    if not arch:
        parser.error('supported hosts: amd64 or arm64')
    image = f'ifshare-minio:7aac2a2c5b7c-{arch}'
    STATE.mkdir(mode=0o700, exist_ok=True)
    STATE.chmod(0o700)
    credentials_file = STATE / 'credentials.json'
    if credentials_file.exists():
        credentials = json.loads(credentials_file.read_text())
    else:
        credentials = {'accessKey': 'ifshare-local', 'secretKey': secrets.token_urlsafe(32)}
        private_file(credentials_file, json.dumps(credentials) + '\n')
    private_file(STATE / 'minio.env', f'MINIO_ROOT_USER={credentials["accessKey"]}\nMINIO_ROOT_PASSWORD={credentials["secretKey"]}\n')
    available = subprocess.run(['docker', 'image', 'inspect', image], stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL).returncode == 0
    if not available:
        env = dict(os.environ, GOTOOLCHAIN='go1.26.5')
        result = run(['go', 'mod', 'download', '-json', f'github.com/minio/minio@{VERSION}'], cwd=ROOT, env=env, capture_output=True, text=True)
        source = Path(json.loads(result.stdout)['Dir'])
        build = STATE / 'image'
        build.mkdir(exist_ok=True)
        (build / 'tmp').mkdir(mode=0o1777, exist_ok=True)
        (build / 'tmp').chmod(0o1777)
        run(['go', 'build', '-trimpath', '-ldflags=-s -w', '-o', build / 'minio', '.'], cwd=source,
            env=dict(env, GOOS='linux', GOARCH=arch, CGO_ENABLED='0'))
        shutil.copyfile(source / 'LICENSE', build / 'LICENSE')
        shutil.copyfile(HERE / 'Dockerfile', build / 'Dockerfile')
        run(['docker', 'build', '-t', image, build])
    existing = subprocess.run(['docker', 'inspect', '--format', '{{json .}}', NAME], capture_output=True, text=True)
    if existing.returncode == 0:
        container = json.loads(existing.stdout)
        if container['Config']['Image'] != image:
            parser.error(f'{NAME} already exists with a different image; it was not changed')
        expected = {'9000/tcp': [{'HostIp': '127.0.0.1', 'HostPort': str(args.port)}],
                    '9001/tcp': [{'HostIp': '127.0.0.1', 'HostPort': str(args.console_port)}]}
        if container['HostConfig']['PortBindings'] != expected:
            parser.error(f'{NAME} already exists with different ports; use its original ports')
        run(['docker', 'start', NAME])
    else:
        for port in (args.port, args.console_port):
            with socket.socket() as sock:
                sock.bind(('127.0.0.1', port))
        run(['docker', 'run', '-d', '--name', NAME, '--restart', 'unless-stopped',
             '-p', f'127.0.0.1:{args.port}:9000', '-p', f'127.0.0.1:{args.console_port}:9001',
             '--env-file', STATE / 'minio.env', '-v', f'{VOLUME}:/data', image])
    endpoint = f'http://127.0.0.1:{args.port}'
    deadline = time.monotonic() + 60
    while True:
        try:
            with urllib.request.urlopen(endpoint + '/minio/health/ready', timeout=2) as response:
                if response.status == 200:
                    break
        except (OSError, urllib.error.URLError):
            pass
        if time.monotonic() >= deadline:
            raise RuntimeError('MinIO did not become ready; check docker logs ifshare-minio')
        time.sleep(0.5)
    settings = {'IFSHARE_S3_ENDPOINT': endpoint, 'IFSHARE_S3_BUCKET': 'ifshare',
                'IFSHARE_S3_REGION': 'us-east-1', 'IFSHARE_S3_CREDENTIALS_FILE': str(credentials_file)}
    run(['go', 'run', './dev/minio/setup'], cwd=ROOT, env=dict(os.environ, **settings))
    private_file(STATE / 'ifshare-s3.env', ''.join(f'export {key}={shlex.quote(value)}\n' for key, value in settings.items()))
    print(f'S3 endpoint: {endpoint}\nConsole: http://127.0.0.1:{args.console_port}')
    print(f'Credentials: {credentials_file}\nEnvironment: {STATE / "ifshare-s3.env"}')


if __name__ == '__main__':
    main()
