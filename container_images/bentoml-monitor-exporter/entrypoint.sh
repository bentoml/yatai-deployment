#!/bin/bash
set -x
set -e
cd $FLUENTBIT_ENTRYPOINT_HOME
./gen-config.pl
/fluent-bit/bin/fluent-bit -c $FLUENTBIT_ENTRYPOINT_HOME/conf/main.conf
