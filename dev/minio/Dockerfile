FROM scratch
COPY minio /usr/local/bin/minio
COPY LICENSE /LICENSE
COPY tmp /tmp
ENV HOME=/tmp MINIO_UPDATE=off
EXPOSE 9000 9001
ENTRYPOINT ["/usr/local/bin/minio"]
CMD ["server", "/data", "--console-address", ":9001"]
