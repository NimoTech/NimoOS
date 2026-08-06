#!/bin/bash
echo "🚀 Starting NimoOS deep self-heal..."

# 1. Kill any old process that may be hung or holding files open
sudo pkill -9 nimoos
echo "✅ Old process stopped"

# 2. Fix the corrupted path config file (repair the loop caused by scanning /)
echo "🛠️ Fixing config file..."
sudo tee /var/lib/casaos/path_config.json <<INNER
{
  "app_data": "/DATA/AppData",
  "images": "/DATA/.docker",
  "database": "/DATA"
}
INNER

# 3. Break the infinite-loop chain and clean up the physical environment
echo "🧹 Cleaning up physical links..."
sudo rm -rf /DATA/AppData /DATA/Gallery /DATA/Downloads /DATA/Documents /DATA/Media /AppData /Gallery /Downloads /Documents /Media
sudo mkdir -p /DATA/AppData /DATA/Gallery /DATA/Downloads /DATA/Documents /DATA/Media
sudo chown -R $USER:$USER /DATA/AppData /DATA/Gallery /DATA/Downloads /DATA/Documents /DATA/Media

# 4. Force rebuild and replace the binary (inject the new patch)
echo "🔨 Building and deploying new version..."
cd /home/wiwiwilliam/workspace/casaos-dev/NimoOS
go build -o nimoos .
sudo rm -f /usr/bin/nimoos
sudo cp nimoos /usr/bin/nimoos
sudo chmod +x /usr/bin/nimoos

echo "🎉 Self-heal script finished!"
echo "Run manually: /usr/bin/nimoos &"
echo "Or refresh the browser to see the fix take effect."
