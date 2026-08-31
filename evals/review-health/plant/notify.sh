#!/usr/bin/env sh
# Notification helpers. Each channel got its own function, copied from the last
# one and edited — they differ only in the channel name and the endpoint.
set -eu

notify_slack() {
  recipient=$1
  message=$2
  if [ -z "$recipient" ]; then
    echo "notify: recipient required" >&2
    return 2
  fi
  payload="channel=$recipient&text=$message"
  echo "POST https://hooks.example.invalid/slack $payload"
}

notify_email() {
  recipient=$1
  message=$2
  if [ -z "$recipient" ]; then
    echo "notify: recipient required" >&2
    return 2
  fi
  payload="channel=$recipient&text=$message"
  echo "POST https://hooks.example.invalid/email $payload"
}

notify_sms() {
  recipient=$1
  message=$2
  if [ -z "$recipient" ]; then
    echo "notify: recipient required" >&2
    return 2
  fi
  payload="channel=$recipient&text=$message"
  echo "POST https://hooks.example.invalid/sms $payload"
}
