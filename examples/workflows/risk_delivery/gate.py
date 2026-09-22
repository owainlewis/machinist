"""Trusted worker-side policy. No repository code is imported or executed here."""
import json
import os
import re
import subprocess
import sys
from pathlib import Path


def gh(*args):
    return json.loads(subprocess.check_output(['gh', *args], text=True, timeout=90))


def reference(value):
    match = re.fullmatch(r'https://github.com/([\w.-]+/[\w.-]+)/pull/([0-9]+)', value)
    if not match:
        raise ValueError('Expected a GitHub PR URL')
    return match.group(1), int(match.group(2))


def validate(pr, assessment, head):
    if assessment.get('risk') not in ('low', 'medium', 'high'):
        raise ValueError('Missing or invalid risk classification')
    if not isinstance(assessment.get('reason'), str) or not assessment['reason'].strip():
        raise ValueError('Risk explanation is required')
    if not re.fullmatch('[0-9a-f]{40}', pr.get('head_sha', '')):
        raise ValueError('Invalid reviewed SHA')
    if assessment.get('head_sha') != pr['head_sha'] or head != pr['head_sha']:
        raise ValueError('PR changed since build or review; rebuild and reassess')


def auto_eligible(risk, files, additions, deletions):
    # Conservative v1: only small Markdown/text documentation changes.
    return (risk == 'low' and 0 < len(files) <= 10 and additions + deletions <= 200
            and all((p.startswith('docs/') or p in ('README.md', 'CHANGELOG.md'))
                    and p.endswith(('.md', '.txt')) for p in files))


def run(mode, inputs):
    if mode not in ('gate', 'merge'):
        raise ValueError('Unknown policy action')
    pr = json.loads(Path(inputs['pr']).read_text())
    assessment = json.loads(Path(inputs['risk']).read_text())
    repo, number = reference(pr['url'])
    if repo != sys.argv[2]:
        raise ValueError('PR repository differs from the worker allowlist')
    data = gh('api', f'repos/{repo}/pulls/{number}')
    validate(pr, assessment, data['head']['sha'])
    if mode == 'merge':
        decision = json.loads(Path(inputs['decision']).read_text())
        if decision.get('pr') != pr or decision.get('assessment') != assessment or type(decision.get('automatic')) is not bool:
            raise ValueError('Merge decision does not match reviewed inputs')
        if data.get('merged'):
            return {'outcome': 'complete', 'summary': f'Already merged {pr["url"]} at reviewed head {pr["head_sha"]}.'}
        if decision.get('base_sha') != data['base']['sha']:
            raise ValueError('Base changed since policy review; reassess before merging')
    if data['state'] != 'open' or data['draft'] or data['base']['ref'] != 'main':
        raise ValueError('PR must be open, non-draft and target main')
    if data['head']['repo']['full_name'] != repo:
        raise ValueError('Fork PRs require a separate merge process')
    if mode == 'gate':
        files = gh('api', '--paginate', '--slurp', f'repos/{repo}/pulls/{number}/files?per_page=100')
        paths = [f['filename'] for page in files for f in page]
        if len(paths) != data['changed_files']:
            raise ValueError('Incomplete changed-file listing')
        approved = auto_eligible(assessment['risk'], paths, data['additions'], data['deletions'])
        # Renames and executable/symlink changes are not documentation-only.
        if any(f.get('status') == 'renamed' for page in files for f in page):
            approved = False
        commit = gh('api', f'repos/{repo}/git/trees/{pr["head_sha"]}?recursive=1')
        if commit.get('truncated'):
            approved = False
        modes = {x['path']: x['mode'] for x in commit['tree']}
        if any(modes.get(f['filename']) != '100644' for page in files for f in page if f.get('status') != 'removed'):
            approved = False
        receipt = dict(pr=pr, assessment=assessment, automatic=approved, base_sha=data['base']['sha'])
        Path(os.environ['MACHINIST_OUTPUT_DIR'], 'decision.json').write_text(json.dumps(receipt, indent=2))
        return {'outcome': 'complete', 'approval_required': not approved,
                'summary': f'{assessment["risk"].capitalize()} risk: {assessment["reason"]}. ' + ('Eligible for automatic merge after checks.' if approved else 'Human approval required before merge.')}
    # This command runs only after the control plane has released the merge stage.
    if data.get('mergeable') is not True or data.get('mergeable_state') != 'clean':
        raise ValueError('PR is not cleanly mergeable')
    checks = gh('pr', 'view', pr['url'], '--json', 'statusCheckRollup,reviewDecision,headRefOid,baseRefOid')
    if checks.get('headRefOid') != pr['head_sha'] or checks.get('baseRefOid') != decision['base_sha']:
        raise ValueError('PR changed while checking merge readiness')
    if checks['reviewDecision'] in ('CHANGES_REQUESTED', 'REVIEW_REQUIRED'):
        raise ValueError('Changes have been requested')
    states = checks['statusCheckRollup']
    if not states or any((c.get('conclusion') not in ('SUCCESS', 'NEUTRAL', 'SKIPPED') or c.get('status') != 'COMPLETED') if c.get('__typename') == 'CheckRun' else c.get('state') != 'SUCCESS' for c in states):
        raise ValueError('All reported checks must finish successfully; missing checks block merging')
    owner, name = repo.split('/')
    query = 'query($owner:String!,$name:String!,$number:Int!){repository(owner:$owner,name:$name){pullRequest(number:$number){reviewThreads(first:100){nodes{isResolved}pageInfo{hasNextPage}}}}}'
    threads = gh('api', 'graphql', '-f', f'query={query}', '-f', f'owner={owner}', '-f', f'name={name}', '-F', f'number={number}')['data']['repository']['pullRequest']['reviewThreads']
    if threads['pageInfo']['hasNextPage'] or any(not t['isResolved'] for t in threads['nodes']):
        raise ValueError('Unresolved or uninspected review threads')
    subprocess.run(['gh', 'pr', 'merge', pr['url'], '--squash', '--match-head-commit', pr['head_sha']], check=True, timeout=90)
    if not gh('api', f'repos/{repo}/pulls/{number}').get('merged'):
        raise ValueError('GitHub has not confirmed the merge')
    return {'outcome': 'complete', 'summary': f'Merged {pr["url"]} at reviewed head {pr["head_sha"]}.'}


if __name__ == '__main__':
    try:
        result = run(sys.argv[1], json.load(sys.stdin))
    except Exception as exc:
        result = {'outcome': 'blocked', 'summary': str(exc)}
    Path(os.environ['MACHINIST_OUTPUT_DIR'], 'report.md').write_text(result['summary'] + '\n')
    Path(os.environ['MACHINIST_STEP_RESULT_PATH']).write_text(json.dumps(result))
