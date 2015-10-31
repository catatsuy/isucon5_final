#!/bin/bash -ex

DEPLOY_USER=$1

scp $DEPLOY_USER@isu11c:~/isucon5_final/golang/app ./app
for SERVER in isu11a isu11b isu11c
do
  scp ./app isucon@$SERVER:
done
