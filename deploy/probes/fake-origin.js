const http=require('http');const dgram=require('dgram');
http.createServer((q,r)=>r.end('synthetic origin')).listen(18081,'0.0.0.0');
// A live DNS responder makes the negative external-DNS probe meaningful.
const udp=dgram.createSocket('udp4');
udp.on('message',(q,peer)=>{
 if(q.length<12)return;
 const header=Buffer.from(q.subarray(0,12));header.writeUInt16BE(0x8180,2);header.writeUInt16BE(1,6);
 const answer=Buffer.from([0xc0,0x0c,0,1,0,1,0,0,0,1,0,4,127,0,0,9]);
 udp.send(Buffer.concat([header,q.subarray(12),answer]),peer.port,peer.address);
});udp.bind(53,'0.0.0.0');
