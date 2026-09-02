# Build and run the single relab binary. The image is the same for the control
# plane and the workers; the command decides which one it is.

FROM golang:1.23-alpine AS build
WORKDIR /src

# Dependencies are copied and downloaded first so that a source-only change
# does not re-download the module cache.
COPY go.mod go.sum ./
RUN go mod download

COPY . .
ARG VERSION=dev
RUN CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X main.version=${VERSION}" -o /out/relab ./cmd/relab

FROM alpine:3.20
# A non-root user: the process needs nothing but an outbound database
# connection, and running as root would be a gratuitous risk.
RUN adduser -D -u 10001 relab
COPY --from=build /out/relab /usr/local/bin/relab
# Example workflows and scenarios ship in the image so the quick start needs no
# bind mount.
COPY --from=build /src/examples /examples
COPY --from=build /src/testdata /testdata
USER relab
ENTRYPOINT ["relab"]
CMD ["server"]
