package kr.scin.rishmcp;

interface IUserService {
    // Reserved by Shizuku for stopping a UserService process.
    void destroy() = 16777114;

    // Runs `sh -c <cmd>` as uid 2000 and returns a JSON ShellResult.
    String exec(String cmd, long timeoutMs) = 1;
}
