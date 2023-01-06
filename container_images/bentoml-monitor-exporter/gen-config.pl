#!/usr/bin/perl

open(OUT, ">conf/output.conf") || die "Can't open config file for writing: $!";

print OUT "[OUTPUT]\n";
print OUT "    Name \${FLUENTBIT_OUTPUT}\n";
print OUT "    Match \${FLUENTBIT_OUTPUT_MATCH}\n";

foreach (sort keys %ENV) { 
	if (substr($_, 0, 24) eq 'FLUENTBIT_OUTPUT_OPTION_') {
		print OUT lc("    " . substr($_, 24)) . " " . $ENV{$_} . "\n";
	}
}
exit 0;
