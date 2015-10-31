#!/bin/bash -ex

DEPLOY_USER=$1

scp $DEPLOY_USER@isu11c:~/isucon5_final/golang/app ./app
for SERVER in isu11a isu11b isu11c
do
  ssh -t isucon@$SERVER sudo supervisorctl stop golang
  scp ./app isucon@$SERVER:
  ssh -t isucon@$SERVER sudo supervisorctl start golang
done
