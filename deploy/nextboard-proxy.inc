# Shared proxy settings for deploy/nginx.conf.example; install as
# /etc/nginx/nextboard-proxy.inc.
proxy_http_version 1.1;

# The hub compares Origin with Host and builds its CSP from it. $http_host keeps
# the port, which preview listeners need.
proxy_set_header Host $http_host;

# Decides the Secure cookie flag, CSP scheme and pairing URLs. The hub only
# believes this header from a peer on 127.0.0.1, so nginx must run on the same
# host. Always overwrite it here: never pass a client's value through.
proxy_set_header X-Forwarded-Proto $scheme;
proxy_set_header X-Forwarded-For $remote_addr;

# WebSockets: terminals and events stay open for a long time.
proxy_set_header Upgrade $http_upgrade;
proxy_set_header Connection $nextboard_connection;
proxy_read_timeout 1d;
proxy_send_timeout 1d;

# Terminal output and job progress must not sit in a buffer.
proxy_buffering off;
