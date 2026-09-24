# Sourced by evals/run.sh and evals/plugin-eval.sh: the operator's tool grant,
# the same under both runners.
#
# `claude plugin eval` gives a case exactly these tools plus the ungated ones
# its own execution.allowed_tools names, and withholds every other tool from
# the model. run.sh builds the same set (grade.py meta). Bash, Write and Edit
# are the gated tools the skills need. Skill(claude-api) is review-health's
# prompt audit. ListAgents is how implement-issue's review gate confirms a
# backgrounded /code-review has finished (issue #472).
eval_grants=(Bash Write Edit 'Skill(claude-api)' ListAgents)
