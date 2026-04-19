# Docker Bridge Network Overhead for Kafka Consumer at 1GB/s Throughput

## 1. Docker Bridge Network Latency Per Packet at High Throughput

### Packet path through bridge network
- Container eth0 → veth pair → docker0 bridge → iptables NAT → host network namespace
- Each packet traverses: network namespace switch, veth pair, bridge forwarding, NAT/conntrack lookup

### Per-packet latency overhead
- **veth pair + bridge**: adds ~20-50µs latency per packet (oneuptime.com benchmark, modern Linux server)
- **NAT traversal (DNAT+SNAT)**: adds conntrack lookup ~1-5µs per packet for established connections
- **docker-proxy (userspace)**: when connecting via `localhost`, adds significant overhead — median response time increased from ~120ms to ~280ms in LLM streaming tests (vipinpg.com)
- **Total bridge overhead vs host**: ~5-10% higher latency in high-concurrency scenarios (eastondev.com)

### Key insight from LLM inference debugging (vipinpg.com)
- Bridge mode with high-throughput small-packet streams (50 tokens/sec SSE chunks): latency spikes 200-300ms
- Root cause: veth pair + bridge traversal itself, NOT MTU or iptables NAT
- Each packet crosses: container namespace → veth → docker0 bridge → host namespace → loopback
- Context switches and buffer copies per packet are the bottleneck

## 2. Can Bridge Network Handle 1GB/s Throughput?

### iperf3 Benchmark Data (10GbE physical link)

| Scenario | Throughput | Overhead vs Host | Source |
|----------|-----------|-------------------|--------|
| Host network | 10.3 GB/s | baseline | dbi-services.com |
| Docker host network | 10.3 GB/s | ~0% | dbi-services.com |
| Docker bridge network | 8.93 GB/s | **-13%** | dbi-services.com |
| Docker bridge + proxy (localhost) | 7.37 GB/s | **-28%** | dbi-services.com |
| Docker overlay network | 7.04 GB/s | **-32%** | dbi-services.com |

### Multi-host bridge benchmarks (Percona, 10GbE)
| Client | Server | Throughput (tps) | Ratio |
|--------|--------|------------------|-------|
| Direct | Direct | 282,780 | 1.00 |
| Direct | Host | 280,622 | 0.99 |
| Direct | Bridge | 250,104 | **0.88** |
| Bridge | Bridge | 235,052 | **0.83** |

### Key findings for 1GB/s scenario
- Bridge network CAN handle 1GB/s — observed 8.93 GB/s on 10GbE link
- At 1GB/s (~125 MB/s), bridge is NOT the bottleneck for bulk throughput
- **However**: the overhead is per-packet, not per-byte. Small messages = more packets = more overhead
- NAT/conntrack becomes CPU-bound before bandwidth-bound

### iptables NAT + conntrack limits
- **conntrack module is the NAT bottleneck** (github.com/moby/moby issue #7857)
- With NAT: 213,721 trans/s vs no-docker 742,020 trans/s = **71% degradation** (netperf, 1-byte packets)
- With bridge only (no NAT): 432,079 trans/s = **42% degradation**
- Tuning `veth txqueuelen=0` improves bridge-only to 704,440 trans/s (~5% overhead)
- NAT with tuned veth: 243,145 trans/s — conntrack remains bottleneck

### Conntrack table pressure
- Default `nf_conntrack_max`: auto-sized based on RAM (~64 connections per MB)
- Systems >1GB RAM: default capped at 65,536 entries
- Each conntrack entry: ~300-450 bytes kernel memory
- At 1GB/s with many concurrent connections: table fills → **packets silently dropped**
- Kafka consumer: typically 1 TCP connection per broker → low conntrack pressure per consumer
- Symptom: `nf_conntrack: table full, dropping packet` in dmesg
- Fix: `sysctl -w net.netfilter.nf_conntrack_max=524288` or higher

## 3. Latency Difference: Bridge localhost:9092 vs Host Network

### Architecture comparison
- **Bridge via localhost:9092**: host → docker-proxy (userspace Go process) → container OR host → iptables DNAT → container
- **Host network**: direct kernel TCP socket, no virtual networking layer

### Measured differences

| Metric | Host Network | Bridge Network | Delta |
|--------|-------------|----------------|-------|
| iperf3 throughput | ~40 Gbps | ~20-30 Gbps | 25-50% lower |
| Latency overhead | ~0µs | ~20-50µs per packet | +20-50µs |
| Redis benchmark (req/s) | 260,920 | 117,744 | **2.2x slower** (via localhost) |
| HTTP median response | 120ms | 280ms | **2.3x slower** (high-throughput streaming) |

### docker-proxy impact
- When connecting via `localhost:published_port`, traffic goes through docker-proxy (userspace process)
- docker-proxy adds **28% throughput degradation** vs direct bridge IP access
- Solution: set `"userland-proxy": false` in daemon.json — forces iptables-only routing
- Or connect via container IP directly (172.x.x.x) to bypass proxy entirely

### Practical latency for Kafka consumer
- Kafka consumer fetching from broker in same Docker bridge network:
  - Bridge internal (container-to-container): ~0.065-0.072ms RTT (ping benchmark)
  - Bridge via published port (localhost): ~0.1-0.5ms RTT with docker-proxy overhead
  - Host network: ~0.01-0.05ms RTT (native loopback)
- **Expected difference at 1GB/s**: bridge adds ~50-200µs per fetch request vs host network
- For large batch fetches (fetch.min.bytes=1MB+), latency overhead is amortized
- For small frequent fetches, overhead compounds significantly

## 4. Kafka Consumer Group: Unique Groups, 1 Partition — Bottleneck at 1GB/s?

### How it works
- Each consumer in a **unique group** gets ALL messages from the partition independently
- This is Kafka's pub-sub fan-out pattern: different consumer groups = independent subscriptions
- Each group maintains its own offset — no coordination between groups

### Throughput implications at 1GB/s
- **Broker-side**: single partition must serve data to N consumer groups simultaneously
- Each consumer group fetches independently → broker sends same data N times
- With 3 consumer groups at 1GB/s each: broker must read from disk/page cache at 3GB/s
- Single partition throughput limit: ~50K-100K messages/sec (zheteng.pages.dev benchmark)
- In MB/s: single partition can sustain multiple GB/s for large messages (limited by disk I/O or NIC)

### Is this a bottleneck?
- **YES, if**: consumers are on bridge network and connect via localhost → docker-proxy multiplies overhead N times
- **NO, if**: consumers use host network or bridge internal IP — broker just reads from page cache N times
- **Broker CPU**: becomes bottleneck — each fetch request requires network I/O + data copy
- **Page cache advantage**: Kafka serves from OS page cache, so repeated reads of same data are fast (memory-speed)
- **Practical limit**: ~5-10 consumer groups per partition at 1GB/s before broker CPU/network saturates
- **Partition is already the throughput bottleneck**: adding consumers in unique groups doesn't help partition throughput — each group independently reads the same sequential data

### Mitigation strategies
- Use host network for consumers near the broker
- Increase `fetch.min.bytes` and `fetch.max.bytes` to reduce request frequency
- Use `max.poll.records` to batch more messages per poll cycle
- Consider multiple partitions if producer can distribute load

## 5. Practical Throughput Limits of Docker Bridge Networking

### iperf3 Benchmark Summary (various sources)

| Test | Network Mode | Throughput | Notes |
|------|-------------|-----------|-------|
| 10GbE, single stream | Host | 9.90 Gbps | Baseline, 7 retransmissions |
| 10GbE, single stream | Bridge + NAT | 8.60 Gbps | 67,701 retransmissions (github #33115) |
| 10GbE, single stream | Host | 10.3 GB/s | dbi-services benchmark |
| 10GbE, single stream | Bridge | 8.93 GB/s | 13% overhead (dbi-services) |
| Localhost, macOS | Host-to-Host | 56.3 Gbps | Loopback MTU 16384 |
| Localhost, macOS | Host-to-Container | 350 Mbps | Bridge MTU 1500, no offloads |
| Bridge container-to-container | Bridge internal | 4.1 Gbps | compile-n-run benchmark |
| 10GbE, sysbench | Direct-Direct | 282,780 tps | Percona benchmark |
| 10GbE, sysbench | Bridge-Bridge | 235,052 tps | 17% overhead |

### Key throughput limits
- **Bridge overhead**: consistent 12-17% throughput reduction vs host network
- **docker-proxy (localhost access)**: additional 15% reduction (total ~28% vs host)
- **Retransmissions**: bridge + NAT causes 10,000x more TCP retransmissions vs host (67K vs 7 in 30s test)
- **CPU saturation**: at high PPS, SoftIRQ CPU reaches 95%+ with bridge NAT

### At 1GB/s specifically
- 1GB/s = ~125 MB/s = well within bridge network capacity (observed 8+ GB/s peaks)
- **Not a bandwidth bottleneck** at 1GB/s
- **Becomes a latency/CPU bottleneck** with:
  - Many small messages (high PPS)
  - Multiple consumers in bridge network
  - localhost access via docker-proxy
  - CPU already near saturation from Kafka processing

### Recommendations for 1GB/s Kafka consumer scenario
- **Use host network** if: latency-sensitive, small messages, multiple consumer groups
- **Use bridge network** if: isolation needed, large messages, single consumer, can tolerate 50-200µs extra latency
- **Tune if using bridge**:
  - `--userland-proxy=false` in daemon.json (skip docker-proxy)
  - Connect via container IP (172.x.x.x) not localhost
  - `sysctl -w net.netfilter.nf_conntrack_max=524288`
  - Set veth `txqueuelen=0` or higher
  - Increase `fetch.min.bytes` to reduce PPS
- **Alternative**: macvlan/ipvlan driver — near-native performance with isolation

## Sources
- dbi-services.com: Docker network driver performance (iperf3 on 10GbE)
- vipinpg.com: Docker bridge latency in high-throughput LLM streaming
- github.com/moby/moby/issues/7857: NAT + conntrack bottleneck analysis
- github.com/moby/moby/issues/33115: TCP retransmissions with bridge network
- dzone.com (Vadim Tkachenko): Multi-host Docker network performance
- eastondev.com: Bridge vs host network comparison
- oneuptime.com: Bridge vs host performance benchmarks
- didi-thesysadmin.com: iptables/nftables PPS performance, conntrack tuning
- zheteng.pages.dev: Kafka partition throughput limits
- github.com/TechEmpower/FrameworkBenchmarks/issues/6486: System-wide bridge overhead
