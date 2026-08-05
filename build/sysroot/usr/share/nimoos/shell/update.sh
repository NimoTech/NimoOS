#!/bin/bash
###
 # @Author:  LinkLeong link@icewhale.com
 # @Date: 2022-06-30 10:08:33
 # @LastEditors: LinkLeong
 # @LastEditTime: 2022-09-01 22:33:06
 # @FilePath: /NimoOS/build/sysroot/usr/share/nimoos/shell/update.sh
 # @Description:
### 


# Fetches the current updater from the release bucket. This used to curl a
# raw.githubusercontent.com path that the CasaOS→NimoOS rename invented and
# that nothing ever answered on, so running this script simply failed. The
# updater now lives in NimoOS-Build and is published to the bucket below; see
# that repository's README for the full install and update path.
curl -fsSL https://nimoos-public.s3.us-east-2.amazonaws.com/get/nimoos-update.sh | sudo bash
