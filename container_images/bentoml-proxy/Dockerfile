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

ARG USERNAME=yetone
ARG USER_UID=1000
ARG USER_GID=$USER_UID

# Create the user
RUN groupadd --gid $USER_GID $USERNAME \
    && useradd --uid $USER_UID --gid $USER_GID -m $USERNAME

USER $USER_UID:$USER_GID

