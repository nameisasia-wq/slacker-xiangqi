package ai

// Original concise briefing of rules and common strategic ideas. References:
// https://www.wxf-xiangqi.org/images/wxf-rules/2018_World_XiangQi_Rules_English2018.pdf
// https://www.wxf-xiangqi.org/images/free_download_books/xiangqi_introduction_chessplayers_20150323.pdf
// https://www.xqarena.com/zh-hans/guides/opening-principles
// https://www.zh.xiangqi.com/opening-elephant.html
// This stays in the system context when the rolling conversation is truncated.
const chessPrimer = `
赛前棋理简报：先理解这些知识，再结合当前局面选定作战方向；棋理是参考，具体攻防优先。
规则：车沿直线走，不能越子；马走日，先直一步的位置被占即蹩马腿；相走田，中心被占即塞象眼，不能过河；士斜走一步、将帅直走一步，都限于九宫；将帅不能无遮挡照面。炮不吃子时走法如车，吃子必须隔恰好一个炮架；兵卒未过河只前进，过河可横走，不能后退、不能升变。被将军必须解将，无棋可走也判负。本程序未实现重复局面和长将长捉裁决，不把来回将军当作取胜方案。
开局任务：疏通车路和马路，发展主力、协调两翼、争夺要道并保护将帅；无必要不反复移动同一子，不为吃边兵耽误出子，也不把所有兵都推进当作布局。
常见体系及目的：
中炮：炮占中线施压，配合车马争先；后续要有主力接应，不孤炮冒进。
屏风马：面对中炮时以双马护中，组织车炮反击；稳住中路并不意味着被动等待。
飞相：先巩固中路，结合对手布阵出动车马，寻找反击方向；不能只补士相而放弃主力发展。
仙人指路：先挺三路或七路兵试探、疏通马路，再根据对手回应组织阵形。
起马：先发展马，保留后续阵形选择，同时为车炮协调创造条件。
体系可随双方着法转化，不能仅凭开局名称照搬记忆中的坐标或固定招法。攻势、稳健、反击都是赢棋的手段，按局势选择。
中局任务：看车线、马的落点和马腿、炮架、将帅弱点及局部子力配合；每次运子应改善位置、制造威胁或完成防守，为后续协同创造条件。吃子考虑回吃，弃子要有补偿；优势时兼顾简化和反击风险。
残局任务：按剩余子力及位置制定目标；重视过河兵、将帅助攻和关键路线，炮要保留可用炮架，马要疏通活动空间。兵到底线不能升变，不机械推进兵；换子前判断剩余力量是否仍有取胜办法。
阶段衔接：根据主力发展、交战和剩余子力判断阶段，不只按步数。plan 写当前体系或阶段加具体目标，例如“屏风马：出车争夺肋道”；后续围绕目标配合，局面改变时更新目标。`
