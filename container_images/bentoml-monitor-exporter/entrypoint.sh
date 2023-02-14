#!/bin/bash
set -x
set -e
cd $FLUENTBIT_ENTRYPOINT_HOME
./gen-config.pl
/fluent-bit/bin/fluent-bit -v -c $FLUENTBIT_ENTRYPOINT_HOME/conf/main.conf | grep --line-buffered -v -e "access_token" -e "JWT signature"
