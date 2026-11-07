"""One call to the GitHub REST API. tools/claude_review.py uses it to post the PR comment."""
import json
import urllib.request

API='https://api.github.com'

def call(method,path,token,body=None):
    request=urllib.request.Request(API+path,method=method,data=json.dumps(body).encode() if body is not None else None,headers={'Authorization':'Bearer '+token,
        'Accept':'application/vnd.github+json','X-GitHub-Api-Version':'2022-11-28','Content-Type':'application/json'})
    with urllib.request.urlopen(request,timeout=30) as response:return json.loads(response.read() or b'null')
