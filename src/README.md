```bash
# https://github.com/grafana/tempo/issues/4139

{nestedSetParent<0 && true && resource.service.name != nil} | rate() by(resource.service.name) 


{resource.service.name != nil} | rate() by(resource.service.name) 

tempo-1  | level=info ts=2025-07-20T17:36:49.815049503Z caller=metrics_query_range_handler.go:156 msg="query range request" tenant=single-tenant query="{resource.service.name != nil} | rate() by(resource.service.name)" range_nanos=3600000000000 mode= step=15000000000
tempo-1  | level=warn ts=2025-07-20T17:36:49.818129545Z caller=server.go:2220 msg="GET /querier/api/metrics/query_range?start=1753031209816032170&end=1753033020000000000&step=15s&mode=recent&blockID=&startPage=0&pagesToSearch=0&version=&encoding=&size=0&footerSize=0&q=%7Bresource.service.name+%21%3D+nil%7D+%7C+rate%28%29+by%28resource.service.name%29&exemplars=100 (500) 275.083µs"
tempo-1  | level=warn ts=2025-07-20T17:36:49.819361628Z caller=server.go:2220 msg="GET /querier/api/metrics/query_range?start=1753031209816032170&end=1753033020000000000&step=15s&mode=recent&blockID=&startPage=0&pagesToSearch=0&version=&encoding=&size=0&footerSize=0&q=%7Bresource.service.name+%21%3D+nil%7D+%7C+rate%28%29+by%28resource.service.name%29&exemplars=100 (500) 88.125µs"
tempo-1  | level=info ts=2025-07-20T17:36:49.819744462Z caller=metrics_query_range_handler.go:118 msg="query range response - no resp" tenant=single-tenant duration_seconds=0.004704291 error=null
tempo-1  | level=info ts=2025-07-20T17:36:49.81992467Z caller=handler.go:134 tenant=single-tenant method=GET traceID= url="/api/metrics/query_range?end=1753033009&q=%7Bresource.service.name+%21%3D+nil%7D+%7C+rate%28%29+by%28resource.service.name%29&start=1753029409" duration=4.963625ms response_size=0 status=500
tempo-1  | level=warn ts=2025-07-20T17:36:49.820022795Z caller=server.go:2220 msg="GET /api/metrics/query_range?end=1753033009&q=%7Bresource.service.name+%21%3D+nil%7D+%7C+rate%28%29+by%28resource.service.name%29&start=1753029409 (500) 5.231542ms"
```

```bash
curl -i https://collector.jameelfinance.com.eg/v1/traces -X POST -H "Content-Type: application/json" -d @span.json
curl -i http://localhost:4318/v1/traces -X POST -H "Content-Type: application/json" -d @span.json

docker compose up otel-collector --no-deps -d

docker compose logs otel-collector -f 
```

```bash
ziadh@Ziads-MacBook-Air lgtm % docker inspect --format '{{ index .Config.Labels "com.docker.compose.project" }}/{{ index .Config.Labels "com.docker.compose.service" }}' 1e575ea1b0be
lgtm/otel-collector
```

```bash
while true; do curl --fail http://localhost:3100/ready; sleep 1; done
while true; do curl --fail http://localhost:3200/ready; sleep 1; done

wget --quiet --tries=1 --output-document=- http://127.0.0.1:9009/ready

while true; do wget --quiet --tries=1 --output-document=- http://localhost:9009/ready; sleep 1; done
```

## ToDo:
- [X] Grafana Volumes to save dashboards.
- [X] Loki to MinIO "Why not working!!!".
    - Showed folder named fake!
- [X] Loki and Tempo Volumes.
- [X] Tempo to MinIO.
    - DID NOT show folder named fake!
- [ ] derived fields.
- [ ] Get Mimir Up and running:
    - Instrument the code.
    - Modify Collector to send metrics to Mimir.
- [ ] Correlate Metrics with Traces.
- [ ] OTel Collector.

## Later:
- [ ] Kubernetes.
