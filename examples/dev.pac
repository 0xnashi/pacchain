// Development PAC for pacchain.
//
// Uses only reserved .test domains and the local fake upstreams started by
// `make devenv` (cmd/fakeupstream). Safe to commit; never put real hosts here.
//
//   *.a.stg.test         → stg-a  (ok)
//   *.b.stg.test         → stg-b  (ok)
//   *.deny.test          → deny   (upstream replies 407)
//   *.badgw.test         → badgw  (upstream replies 502)
//   *.hangup.test        → hangup (upstream closes the connection)
//   *.down.test          → nothing listening on :18999
//   *.failover.test      → dead proxy first, then stg-b
//   *.fallback.test      → dead proxy first, then DIRECT
//   *.loop.test          → PAC never returns (pacchain stops it after 3s)
//   everything else      → DIRECT
function FindProxyForURL(url, host) {
  if (dnsDomainIs(host, ".a.stg.test")) return "PROXY 127.0.0.1:18431";
  if (dnsDomainIs(host, ".b.stg.test")) return "PROXY 127.0.0.1:18432";
  if (dnsDomainIs(host, ".deny.test")) return "PROXY 127.0.0.1:18407";
  if (dnsDomainIs(host, ".badgw.test")) return "PROXY 127.0.0.1:18502";
  if (dnsDomainIs(host, ".hangup.test")) return "PROXY 127.0.0.1:18000";
  if (dnsDomainIs(host, ".down.test")) return "PROXY 127.0.0.1:18999";
  if (dnsDomainIs(host, ".failover.test")) return "PROXY 127.0.0.1:18999; PROXY 127.0.0.1:18432";
  if (dnsDomainIs(host, ".fallback.test")) return "PROXY 127.0.0.1:18999; DIRECT";
  if (dnsDomainIs(host, ".loop.test")) { while (true) {} }
  return "DIRECT";
}
