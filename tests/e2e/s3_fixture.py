#!/usr/bin/env python3
"""Read-only S3 protocol fixture, not an S3/OSS authentication implementation.
Real Curvine SDK/server/FUSE exercise this endpoint. Authorization is deliberately
minimal; production IAM and OSS behavior require a separate environment test.
"""
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from urllib.parse import urlsplit, parse_qs
import json, threading, hashlib, re
DATA=b'curvine-read-cache-probe\n'
lock=threading.Lock()
state={'gets':0,'bytes':0,'writes':0,'fail':False,'changed':False}
class Handler(BaseHTTPRequestHandler):
    def log_message(self,*args):pass
    def do_POST(self):
        # This control route exists only in the isolated fixture network.
        if self.path.startswith('/__control/'):
            with lock:
                op=self.path.rsplit('/',1)[-1]
                if op=='down':state['fail']=True
                elif op=='up':state['fail']=False
                elif op=='change':state['changed']=True
                elif op=='restore':state['changed']=False
            self.send_response(204);self.end_headers();return
        self.deny()
    def deny(self):
        with lock:state['writes']+=1
        self.send_response(403);self.end_headers()
    do_PUT=deny
    do_DELETE=deny
    def do_HEAD(self):self.read(True)
    def do_GET(self):self.read(False)
    def read(self,head):
        url=urlsplit(self.path)
        if url.path=='/__stats':
            with lock:body=json.dumps(state).encode()
            self.send_response(200);self.send_header('Content-Length',str(len(body)));self.end_headers();self.wfile.write(body);return
        with lock:fail,changed=state['fail'],state['changed']
        if fail:self.send_response(503);self.end_headers();return
        if 'test-access' not in self.headers.get('Authorization',''):
            self.send_response(403);self.end_headers();return
        query=parse_qs(url.query)
        if 'list-type' in query or url.path.rstrip('/')=='/example-datasets':
            body=b'<ListBucketResult xmlns="http://s3.amazonaws.com/doc/2006-03-01/"><Name>example-datasets</Name><Prefix>immutable-v1/</Prefix><IsTruncated>false</IsTruncated><Contents><Key>immutable-v1/probe.txt</Key><LastModified>2026-01-01T00:00:00.000Z</LastModified><ETag>"fixture"</ETag><Size>25</Size><StorageClass>STANDARD</StorageClass></Contents></ListBucketResult>'
            body=body.replace(b'<Size>25</Size>',f'<Size>{len(DATA)}</Size>'.encode())
            prefix=query.get('prefix',[''])[0]
            if not 'immutable-v1/probe.txt'.startswith(prefix):
                start=body.index(b'<Contents>');end=body.index(b'</Contents>')+len(b'</Contents>')
                body=body[:start]+body[end:]
            self.send_response(200);self.send_header('Content-Type','application/xml');self.send_header('Content-Length',str(len(body)));self.end_headers()
            if not head:self.wfile.write(body)
            return
        if url.path!='/example-datasets/immutable-v1/probe.txt' and not re.fullmatch(r'/example-datasets/immutable-v1/scale-[0-9]+\.txt',url.path):
            self.send_response(404);self.end_headers();return
        content=DATA if not changed else b'Curvine-read-cache-probe\n'
        start,end=0,len(content)-1;code=200
        if 'Range' in self.headers:
            part=self.headers['Range'].removeprefix('bytes=');a,b=part.split('-',1);start=int(a);end=min(int(b) if b else end,end);code=206
            if start>len(content)-1:self.send_response(416);self.end_headers();return
        body=content[start:end+1]
        self.send_response(code);self.send_header('Content-Length',str(len(body)));self.send_header('Last-Modified','Thu, 01 Jan 2026 00:00:00 GMT');self.send_header('ETag','"'+hashlib.md5(content).hexdigest()+'"')
        if code==206:self.send_header('Content-Range',f'bytes {start}-{end}/{len(content)}')
        self.end_headers()
        if not head:
            with lock:state['gets']+=1;state['bytes']+=len(body)
            self.wfile.write(body)
ThreadingHTTPServer(('0.0.0.0',8080),Handler).serve_forever()
