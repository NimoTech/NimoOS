#!/bin/bash
###
# @Author: LinkLeong link@icewhale.org
# @Date: 2022-08-25 11:41:22
 # @LastEditors: LinkLeong
 # @LastEditTime: 2022-08-31 17:54:17
 # @FilePath: /NimoOS/build/scripts/setup/service.d/nimoos/debian/setup-nimoos.sh
# @Description:

# Copyright (c) 2021-2025 IceWhale Technology Co., Ltd.
# Copyright (c) 2026 NimoTech
# Licensed under the Apache License, Version 2.0.
# Modified from the original CasaOS source by NimoTech.
###

set -e

APP_NAME="nimoos"

# copy config files
CONF_PATH=/etc/nimoos
OLD_CONF_PATH=/etc/nimoos.conf
CONF_FILE=${CONF_PATH}/${APP_NAME}.conf
CONF_FILE_SAMPLE=${CONF_PATH}/${APP_NAME}.conf.sample


if [ -f "${OLD_CONF_PATH}" ]; then
    echo "copy old conf"
    cp "${OLD_CONF_PATH}" "${CONF_FILE}"
fi
if [ ! -f "${CONF_FILE}" ]; then
    echo "Initializing config file..."
    cp -v "${CONF_FILE_SAMPLE}" "${CONF_FILE}"
fi

rm -rf /etc/systemd/system/nimoos.service # remove old service file

systemctl daemon-reload

# enable service (without starting)
echo "Enabling service..."
systemctl enable --force --no-ask-password "${APP_NAME}.service"
