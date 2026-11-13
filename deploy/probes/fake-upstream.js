const http=require('http');const net=require('net');
const server=http.createServer((q,r)=>{r.writeHead(405);r.end();});
server.on('connect',(request,client,head)=>{
 if(request.headers['proxy-authorization']!=='Basic '+Buffer.from('synthetic:not-a-real-secret').toString('base64')){client.end('HTTP/1.1 407 Proxy Authentication Required\r\n\r\n');return;}
 const upstream=net.connect({host:process.env.ORIGIN_IP,port:18081});
 upstream.on('connect',()=>{client.write('HTTP/1.1 200 Connection Established\r\n\r\n');if(head.length)upstream.write(head);client.pipe(upstream);upstream.pipe(client);});
 upstream.on('error',()=>client.destroy());client.on('error',()=>upstream.destroy());
});server.listen(18080,'0.0.0.0');
