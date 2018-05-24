FROM golang:1.9

ENV target_dir ./target

WORKDIR ${target_dir}

COPY ${target_dir}/msopenhack-stat-sidecar-linux-amd64 openhack-stat-sidecar

CMD ["./openhack-stat-sidecar"]