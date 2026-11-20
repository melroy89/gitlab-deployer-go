FROM golang:1.27.4-trixie AS go-toolchain

FROM php:8.5-cli-trixie

COPY --from=go-toolchain /usr/local/go /usr/local/go
ENV PATH="/usr/local/go/bin:${PATH}"

ARG VERSION=development
ARG VCS_REF=unknown

LABEL org.opencontainers.image.version=$VERSION \
      org.opencontainers.image.revision=$VCS_REF

ARG DEBIAN_FRONTEND=noninteractive

WORKDIR /app

# PHP CLI includes mbstring and XML. Add the remaining post-deployment extensions.
RUN apt-get update \
 && apt-get install -y --no-install-recommends curl git openssh-client wget libicu-dev libzip-dev \
 && docker-php-ext-install -j "$(nproc)" intl zip \
 && rm -rf /var/lib/apt/lists/*

COPY go.mod go.sum ./
RUN go mod download && go mod verify

COPY *.go ./

RUN CGO_ENABLED=0 GOOS=linux go build -o /app/artifact-deployer

EXPOSE 3042

CMD ["/app/artifact-deployer"]
