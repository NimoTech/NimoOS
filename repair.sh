#!/bin/bash
echo "🚀 开始 NimoOS 深度自愈..."

# 1. 杀掉可能挂起或占据文件的旧进程
sudo pkill -9 nimoos
echo "✅ 已停止旧进程"

# 2. 修正损坏的路径配置文件 (修复 / 扫描导致的转圈)
echo "🛠️ 正在修复配置文件..."
sudo tee /var/lib/casaos/path_config.json <<INNER
{
  "app_data": "/DATA/AppData",
  "images": "/DATA/.docker",
  "database": "/DATA"
}
INNER

# 3. 斩断死循环链路并清理物理环境
echo "🧹 正在清理物理链路..."
sudo rm -rf /DATA/AppData /DATA/Gallery /DATA/Downloads /DATA/Documents /DATA/Media /AppData /Gallery /Downloads /Documents /Media
sudo mkdir -p /DATA/AppData /DATA/Gallery /DATA/Downloads /DATA/Documents /DATA/Media
sudo chown -R $USER:$USER /DATA/AppData /DATA/Gallery /DATA/Downloads /DATA/Documents /DATA/Media

# 4. 强制编译并替换二进制文件（注入新内核补丁）
echo "🔨 正在编译并部署新版本..."
cd /home/wiwiwilliam/workspace/casaos-dev/NimoOS
go build -o nimoos .
sudo rm -f /usr/bin/nimoos
sudo cp nimoos /usr/bin/nimoos
sudo chmod +x /usr/bin/nimoos

echo "🎉 自愈脚本执行完成！"
echo "请手动运行: /usr/bin/nimoos &"
echo "或者刷新浏览器查看修复效果。"
