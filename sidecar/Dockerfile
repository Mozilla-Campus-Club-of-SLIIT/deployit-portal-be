FROM alpine:3.19

# Install base packages (fast, ~5MB)
RUN apk add --no-cache bash nano curl ca-certificates

# Install kubectl (pinned version for stability)
ARG KUBECTL_VERSION=v1.29.0
RUN curl -fsSL -o /usr/local/bin/kubectl \
    "https://dl.k8s.io/release/${KUBECTL_VERSION}/bin/linux/amd64/kubectl" && \
    chmod +x /usr/local/bin/kubectl && \
    kubectl version --client

# Install ttyd (web terminal server)
ARG TTYD_VERSION=1.7.7
RUN curl -fsSL -L -o /usr/local/bin/ttyd \
    "https://github.com/tsl0922/ttyd/releases/download/${TTYD_VERSION}/ttyd.x86_64" && \
    chmod +x /usr/local/bin/ttyd

# Smoke-test both binaries exist and execute
RUN kubectl version --client && ttyd --version

ENTRYPOINT ["/bin/sh"]
