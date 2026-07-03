@echo off
REM 一键把本地 Kiro IDE 的登录凭据导入/更新到 sub2api。
REM 先在 Kiro IDE 登录一次,再双击本文件(或命令行运行)。
REM 需要 python3;可用环境变量覆盖 SUB2API_URL / KIRO_ACCOUNT_NAME 等,见 kiro-import.py 顶部说明。
setlocal
python "%~dp0kiro-import.py" %*
echo.
pause
