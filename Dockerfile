ARG BASE_IMAGE=ubuntu
ARG BASE_IMAGE_VERSION=base-ubuntu20.04

FROM hub.xdxct.com/xdxct-docker/${BASE_IMAGE}:${BASE_IMAGE_VERSION} as builder

RUN apt-get update \
  && apt-get install -y -qq --no-install-recommends \
    wget \
    ca-certificates \
    make \
    gcc \
    g++ \
  && rm -rf /var/lib/apt/lists/*

ARG GOLANG_VERSION=1.19.5

RUN wget -nv -O - https://storage.googleapis.com/golang/go${GOLANG_VERSION}.linux-amd64.tar.gz \
    | tar -C /usr/local -xz

ENV GOPATH /go
ENV PATH $GOPATH/bin:/usr/local/go/bin:$PATH

ENV GOOS=linux\
    GOARCH=amd64

WORKDIR /go/src/kubevirt-device-plugin

COPY . .

RUN make build

FROM hub.xdxct.com/xdxct-docker/debian:stretch-slim

COPY --from=builder /go/src/kubevirt-device-plugin/xdxct-kubevirt-device-plugin /usr/bin/

CMD ["xdxct-kubevirt-device-plugin"]