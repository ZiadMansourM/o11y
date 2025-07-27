```bash title="Logql Queries"
# LogQl
{service_name=~"dice-cli|dice-server"}
{service_name=~".+"} | label_format level=detected_level | trace_id="dc87d4cf2a01dadb7796b8b9ce64bd79" | span_id="2317cc79070bb578"
{service_name=~".+"} | label_format level=detected_level | trace_id="dc87d4cf2a01dadb7796b8b9ce64bd79"

# TraceQl
{}
{resource.service.name=~"dice-cli|dice-server"}
{resource.service.version="v1.0.0" && resource.service.name="dice-server"}

# PromQl
sum without(status_code)(client_requests_total)
sum without(status_code)(http_requests_total)

sum(http_requests_total{status_code=~"2.."}) / sum(http_requests_total) * 100
```
