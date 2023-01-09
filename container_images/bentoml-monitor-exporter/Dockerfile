FROM debian:bullseye-slim as build-env
# fluentbit 2.0.6 is using bullseye
RUN apt-get update && apt-get install -y perl bash

#######################################
FROM cr.fluentbit.io/fluent/fluent-bit:2.0.6

COPY --from=build-env /bin/bash /bin/bash
COPY --from=build-env /lib/x86_64-linux-gnu/libtinfo.so.6 /lib/x86_64-linux-gnu/libtinfo.so.6

COPY --from=build-env /usr/bin/perl /usr/bin/perl
COPY --from=build-env /lib/x86_64-linux-gnu/libm.so.6 /lib/x86_64-linux-gnu/libm.so.6
COPY --from=build-env /lib/x86_64-linux-gnu/libcrypt.so.1 /lib/x86_64-linux-gnu/libcrypt.so.1

ARG USER_UID=1000

ENV FLUENTBIT_ENTRYPOINT_HOME=/fluent-bit/entrypoint
COPY --chown=$USER_UID:$USER_UID . $FLUENTBIT_ENTRYPOINT_HOME

ENV FLUENTBIT_OTLP_PORT=5000
ENV FLUENTBIT_HTTP_PORT=2020
ENV FLUENTBIT_OUTPUT=stdout
ENV FLUENTBIT_OUTPUT_MATCH=*

# some example option env
#ENV FLUENTBIT_OUTPUT_OPTION_FORMAT=msgpack
#ENV FLUENTBIT_OUTPUT_OPTION_WORKERS=3

USER $USER_UID:$USER_UID

ENTRYPOINT []
CMD ["/bin/bash", "-c", "$FLUENTBIT_ENTRYPOINT_HOME/entrypoint.sh"]
