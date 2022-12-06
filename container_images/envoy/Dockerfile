FROM ubuntu:22.04 as builder

RUN apt-get update
RUN apt-get install -y wget
RUN wget https://github.com/envoyproxy/envoy/releases/download/v1.24.1/envoy-1.24.1-linux-x86_64 \
    && chmod +x envoy-1.24.1-linux-x86_64 \
    && mv envoy-1.24.1-linux-x86_64 /usr/local/bin/envoy

FROM ubuntu:22.04
COPY --from=builder /usr/local/bin/envoy /usr/local/bin/envoy
RUN apt-get update
RUN apt-get install -y curl

