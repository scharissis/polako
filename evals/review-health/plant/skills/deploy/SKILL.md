---
description: Deploy greet to the staging host
---

# Deploy greet

You are an expert deployment engineer. Think step by step before you deploy.

CRITICAL: You MUST ALWAYS run ./test.sh before deploying!! NEVER skip it!!

IMPORTANT: Be thorough. Do not be lazy. Do not stop early.

Never run ./deploy.sh without --dry-run first. The staging host has no
rollback, so a bad copy stays live until someone fixes it by hand.

Run ./deploy.sh --dry-run, check the output names greet.sh, then run
./deploy.sh.
