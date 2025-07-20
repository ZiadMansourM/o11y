## Current Status
- We are using prometheus auto instrumentation pkg.
- Loki stores all logs without any context.
- No meaningful alerting.
- Internal state of system is ambiguous.
    - Debugging Hard.
    - Delivery Harder.

## What we are missing
- **Manual Instrumentation**.
- Enriching context.
- **Adapot traces**.
- Correlate the three telmetry signals {traces,logs,metrics}.
- **Metric Design**:
    - Four Golden Metrics "Good Start" {latency,errors,traffic,saturation}.
    - (Business) Custom Metrics "Users/plugin/{time}" "Availability / plugin".
    - Frontend Metrics (Web Vitals):
        - LCP (Largest Contentful Paint).
        - FID (First Input Delay) >>> INP (Interaction to Next Paint).
        - CLS (Cumulative Layout Shift).
- o11y guild

## Demo
1. v0: RollDice Service "No Instrumentation".
2. v1:
    - **DONE** Normal RollDice Service.
    - Aims for:
        - {Manual,Auto} Instrumentation.
            - Auto: context propagation.
            - Manual:
                - Metrics:
                    - Error Rate.
                    - Latency.
                    - Traffic.
                - **DONE** Tracing.
                - **DONE** Logging.
        - Different Telemetry Signals in Action "Debugging / Correlation".
    - What it containes:
        - Each roll results in an array of four numbers if 1 in results consider error.
        - {FE,CLI}>BE>DB
3. v2:
    - RollDice Service v2.
        - RollDice returns only one number "erros out 10% of the time".
    - Decouple Release from Deployment.
        - Only QA users see the new version.
        - We test the new version in production for normal users and report error rate of it.

Then we present real world example:
    - Delivery Hero.
    - elmenus / zalando "funnel report"
