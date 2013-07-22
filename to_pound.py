#!/usr/bin/python3.2
#
# Copyright (c) 2012 Tonnerre Lombard <tonnerre@ancient-solutions.com>,
#                    Ancient Solutions. All rights reserved.
#
# Redistribution and use in source and binary forms, with or without
# modification, are permitted provided that the following conditions
# are met:
#
# 1. Redistributions  of source code must retain  the above copyright
#    notice, this list of conditions and the following disclaimer.
# 2. Redistributions  in   binary  form  must   reproduce  the  above
#    copyright  notice, this  list  of conditions  and the  following
#    disclaimer in the  documentation and/or other materials provided
#    with the distribution.
#
# THIS  SOFTWARE IS  PROVIDED BY  ANCIENT SOLUTIONS  AND CONTRIBUTORS
# ``AS IS'' AND ANY EXPRESS OR IMPLIED WARRANTIES, INCLUDING, BUT NOT
# LIMITED TO,  THE IMPLIED WARRANTIES OF  MERCHANTABILITY AND FITNESS
# FOR A  PARTICULAR PURPOSE  ARE DISCLAIMED.  IN  NO EVENT  SHALL THE
# FOUNDATION  OR CONTRIBUTORS  BE  LIABLE FOR  ANY DIRECT,  INDIRECT,
# INCIDENTAL,   SPECIAL,    EXEMPLARY,   OR   CONSEQUENTIAL   DAMAGES
# (INCLUDING, BUT NOT LIMITED  TO, PROCUREMENT OF SUBSTITUTE GOODS OR
# SERVICES; LOSS OF USE,  DATA, OR PROFITS; OR BUSINESS INTERRUPTION)
# HOWEVER CAUSED AND ON ANY THEORY OF LIABILITY, WHETHER IN CONTRACT,
# STRICT  LIABILITY,  OR  TORT  (INCLUDING NEGLIGENCE  OR  OTHERWISE)
# ARISING IN ANY WAY OUT OF THE USE OF THIS SOFTWARE, EVEN IF ADVISED
# OF THE POSSIBILITY OF SUCH DAMAGE.
#
# Convert a http-reverse-proxy config to a pound configuration file.

from reverse_proxy_config_pb2 import ReverseProxyConfig
from google.protobuf import text_format
import sys

pound_user = 'www-data'
pound_group = 'www-data'
pound_jail = None

log_level = 3
alive = 30
ctrl_socket = '/var/run/pound/poundctl.socket'

conf = ReverseProxyConfig()
f = open(sys.argv[1])
text_format.Merge(f.read(), conf)
f.close()

print("## see pound(8) for details\n"
	"## This is a generated file! Do not modify.\n\n"
	"########################################################\n\n"
	"## global options:\n\n"
	"User\t\t\"%(user)s\"\n"
	"Group\t\t\"%(group)s\"" %
	{'user': repr(pound_user)[1:-1], 'group': repr(pound_group)[1:-1]})

if pound_jail:
	print("RootJail\t\"%s\"" % repr(pound_jail)[1:-1])

print("\n## Logging: (goes to syslog by default)\n##\t0\tno logging\n"
		"##\t1\tnormal\n##\t2\textended\n"
		"##\t3\tApache-style (common log format)\n"
		"LogLevel\t%(log_level)d\n\n"
		"## check backend every X seconds\nAlive\t%(alive)d\n\n"
		"# poundctl control socket\nControl\t\"%(ctrl_socket)s\"\n\n"
		"########################################################\n\n"
		"## listen, redirect and ... to:\n" %
		{'log_level': log_level, 'alive': alive,
			'ctrl_socket': repr(ctrl_socket)[1:-1]})

for p in conf.port_config:
	if p.ssl_cert_path and p.ssl_key_path:
		print("ListenHTTPS")
	else:
		print("ListenHTTP")

	print("\tAddress\t0.0.0.0\n\tPort\t%(port)d" % {'port': p.port})

	if p.ssl_cert_path and p.ssl_key_path:
		print("\tCert\t\"%(cert)s\"\n\tCert\t\"%(key)s\"" %
				{'cert': repr(str(p.ssl_cert_path))[1:-1],
					'key': repr(str(p.ssl_key_path))[1:-1]})

	print("\n\txHTTP\t2\n\tRewriteDestination\t1\nEnd\n")

for t in conf.target_config:
	for h in t.http_host:
		print("Service\n\tHeadRequire\t\"Host:[ \\t]*%(host)s.*\"\n"
				% {'host': h})

		for be in t.be:
			print("\tBackEnd\n\t\tAddress\t%(address)s\n"
					"\t\tPort\t%(port)d\n\tEnd" %
					{'address': be.host, 'port': be.port})

		if len(t.be) == 0 and len(t.backend_uri) > 0:
			print("\tBackEnd\n\t\tAddress\t%(address)s\n"
					"\t\tPort\t%(port)d\n\tEnd" %
					{'address': '::1', 'port': 8080})

		print("End\n")
