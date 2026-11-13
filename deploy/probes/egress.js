// No credentials are read or printed. The CI test supplies a synthetic origin;
// the deployment probe uses public TLS/DNS endpoints without application tokens.
const net = require('net');
const tls = require('tls');
const http = require('http');
const dns = require('dns').promises;
const mode = process.argv[1] || 'working';
const origin = process.env.PROBE_ORIGIN || 'api.github.com';
const port = Number(process.env.PROBE_PORT || 443);
const directHost = process.env.PROBE_DIRECT_HOST || '1.1.1.1';
const directPort = Number(process.env.PROBE_DIRECT_PORT || 443);
function tcp(host, port) {
  return new Promise((resolve, reject) => {
    const socket = net.connect({host, port});
    socket.setTimeout(2000);
    socket.once('connect', () => { socket.destroy(); resolve(); });
    socket.once('error', reject);
    socket.once('timeout', () => { socket.destroy(); reject(new Error('timeout')); });
  });
}
async function mustFail(operation, label) {
  let failed = false;
  try { await operation(); } catch (_) { failed = true; }
  if (!failed) throw new Error(label + ' unexpectedly succeeded');
}
function tunnel() {
  return new Promise((resolve, reject) => {
    const request = http.request({host:'egress', port:3128, method:'CONNECT', path:origin+':'+port});
    request.setTimeout(6000, () => request.destroy(new Error('proxy timeout')));
    request.once('error', reject);
    request.once('response', response => { response.resume(); reject(new Error('CONNECT denied')); });
    request.once('connect', (response, socket, head) => {
      if (response.statusCode !== 200) { socket.destroy(); reject(new Error('CONNECT denied')); return; }
      if (head.length) socket.unshift(head);
      const channel = process.env.PROBE_PLAINTEXT === '1' ? socket : tls.connect({socket, servername:origin, rejectUnauthorized:true});
      channel.setTimeout(6000, () => {channel.destroy();reject(new Error('origin timeout'));});
      channel.once('error', reject);
      const send = () => channel.write('GET / HTTP/1.1\r\nHost: '+origin+'\r\nUser-Agent: ai-taskbot-network-check\r\nConnection: close\r\n\r\n');
      if (channel === socket) send(); else channel.once('secureConnect', send);
      let statusLine = Buffer.alloc(0);
      channel.on('data', data => {
        statusLine = Buffer.concat([statusLine, data]);
        const end = statusLine.indexOf('\r\n');
        if (end < 0 && statusLine.length <= 4096) return;
        const valid = end >= 0 && statusLine.subarray(0,end).toString('latin1').startsWith('HTTP/1.');
        channel.destroy();
        if (valid) resolve(); else reject(new Error('invalid origin response'));
      });
      channel.once('end', () => reject(new Error('empty origin response')));
    });
    request.end();
  });
}
(async () => {
  await mustFail(() => tcp(directHost,directPort), 'Direct TCP');
  await mustFail(() => tcp('2606:4700:4700::1111',443), 'Direct IPv6 TCP');
  const resolver = new dns.Resolver({timeout:1000, tries:1});
  resolver.setServers([directHost]);
  await mustFail(() => resolver.resolve4('example.org'), 'Direct external DNS');
  await mustFail(() => dns.resolve4('example.org'), 'Container external DNS');
  if (process.env.PROBE_WORKER === '1') await tcp('worker',8090);
  if (mode === 'blocked') await mustFail(tunnel,'Proxy connection while proxy stopped');
  else if (mode === 'working') await tunnel();
  else throw new Error('Unknown probe mode');
  console.log('Network checks passed: direct TCP/DNS blocked; proxy '+mode+'.');
})().catch(() => { console.error('Network isolation/proxy verification failed; no application started.'); process.exitCode=1; });
